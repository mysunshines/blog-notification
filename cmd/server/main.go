package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mysunshines/blog-notification/internal/repository"
	"github.com/mysunshines/blog-notification/internal/service"
	"github.com/mysunshines/blog-notification/internal/ws"
	svcconst "github.com/mysunshines/blog-notification/internal/constants"
	notification "github.com/mysunshines/blog-notification/proto/pb/v1"

	"github.com/mysunshines/gocommon/cache"
	goconfig "github.com/mysunshines/gocommon/config"
	"github.com/mysunshines/gocommon/configcenter"
	"github.com/mysunshines/gocommon/constants"
	"github.com/mysunshines/gocommon/consul"
	"github.com/mysunshines/gocommon/database"
	"github.com/mysunshines/gocommon/log"
	"github.com/mysunshines/gocommon/metrics"
	"github.com/mysunshines/gocommon/middleware"
	"github.com/mysunshines/gocommon/observability"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sony/gobreaker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"gorm.io/gorm"
)

// Version 由构建脚本通过 -ldflags "-X main.Version=xxx" 注入，未注入时默认 "dev"。
var Version = "dev"

// 进程级资源句柄，供 shutdown/releaseInfra 在退出时统一释放（避免多处 defer 重复释放）。
var (
	metricsCancel context.CancelFunc
	hotCfg        *configcenter.ServiceConfig
	deregister    func() error
	// serviceName 当前服务名（取自配置 cfg.App.Name），供 main 顶层 defer 与 run 内共用，
	// 避免硬编码 gocommon 的 constants.ServiceNameXxx。
	serviceName string
)

type Server struct {
	cfg        *goconfig.Config
	httpServer *http.Server
	grpcServer *grpc.Server
	db         *gorm.DB
	cb         *gobreaker.CircuitBreaker // 熔断器
	hub        *ws.Hub                   // WebSocket 连接管理（按 userID 分组定向推送）
	notifySvc  *service.NotificationService
	quitCh     chan struct{}
}

// initInfra 负责所有外部基础设施的初始化（数据库、Redis）。
// 与 NewServer（纯依赖装配）分离，使 main 的启动顺序清晰可控。
// 初始化失败返回 error（由调用方统一处理，避免直接 os.Exit 导致资源泄漏）。
// Redis KeyPrefix 强制覆写为 notification: —— 未读计数缓存按服务隔离命名空间，
// 多服务共用同一 Redis 实例时互不干扰。
func initInfra(cfg *goconfig.Config) (*gorm.DB, error) {
	if err := database.Init(&cfg.Database, cfg.App.Env); err != nil {
		return nil, fmt.Errorf("failed to init database: %v", err)
	}
	db := database.GetDB()

	cacheCfg := cfg.Redis
	cacheCfg.KeyPrefix = svcconst.RedisKeyPrefixNotification
	if err := cache.Init(&cacheCfg); err != nil {
		return nil, fmt.Errorf("failed to init Redis: %v", err)
	}

	return db, nil
}

// NewServer 仅做依赖装配（限流器/JWT/熔断器/Hub/仓储/服务），不做任何 I/O。
// Hub 的事件循环 goroutine 在此启动，生命周期与整个 Server 一致；
// 熔断器包裹所有 gRPC 一元调用（入口拦截器），防止单点慢请求拖垮整个服务。
func NewServer(cfg *goconfig.Config, db *gorm.DB) *Server {
	// 初始化限流器
	middleware.InitRateLimiter(&cfg.RateLimit)

	// 初始化 JWT（gRPC 鉴权拦截器与 WS 握手校验共用同一密钥）
	middleware.InitJWT(cfg.JWT.Secret)

	// 初始化熔断器
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        serviceName,
		MaxRequests: constants.DefaultCBMaxRequests,
		Interval:    constants.DefaultCBInterval * time.Second,
		Timeout:     constants.DefaultCBTimeout * time.Second,
	})

	// Hub 维护所有在线 WS 连接（map[userID]set[*Client]），后台事件循环
	// 串行处理注册/注销/推送三类事件，服务关闭时由 Server.shutdown 统一停止。
	hub := ws.NewHub()
	go hub.Run()

	repo := repository.NewNotificationRepository(db)
	svc := service.NewNotificationService(repo, hub)

	return &Server{
		cfg:       cfg,
		db:        db,
		cb:        cb,
		hub:       hub,
		notifySvc: svc,
		quitCh:    make(chan struct{}),
	}
}

// Run 启动三组监听（HTTP/WS、gRPC、Metrics）并阻塞等待退出信号。
// 任一 server goroutine 异常退出都会 close(quitCh)，触发整服务优雅关闭，
// 避免半死状态（如 gRPC 挂了但 HTTP 还活着）继续接收流量。
func (s *Server) Run() error {
	// 启动 HTTP 服务器（WS 升级 + 探活）
	go s.runHTTPServer()

	// 启动 gRPC 服务器（业务入口）
	go s.runGRPCServer()

	// 启动 Prometheus 指标服务器
	if goconfig.Get().Metrics.Enabled {
		go s.runMetricsServer()
	}

	// 等待信号或内部 server 监听失败
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case <-s.quitCh:
		log.Errorf("server goroutine failed, initiating shutdown")
	}

	log.Info("Shutting down server...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.shutdown(ctx); err != nil {
		log.Errorf("server shutdown error: %v", err)
	}

	log.Info("Server exited")
	return nil
}

// runHTTPServer 承载 WebSocket（/ws/notification）与运维探针。
// 业务 REST 仍走 gRPC（gateway 反射代理），本端口的 gin 仅用于 WS 升级与探活。
//
// WebSocket 启动原理：
//
//	WS 复用 HTTP 完成握手——客户端发起普通 HTTP GET，携带
//	Upgrade: websocket / Connection: Upgrade 头；服务端（gorilla/websocket
//	的 upgrader.Upgrade）校验头后返回 101 Switching Protocols，并通过
//	HTTP Hijack 接管底层 TCP 连接，此后该连接不再走 HTTP 语义，改为
//	WS 帧协议（文本/二进制/ping/pong/close）双向通信。
//
//	两个工程要点：
//	  1. WriteTimeout 对升级后的连接无效（Hijack 后超时由 readPump/writePump
//	     的 SetDeadline 自行管理），因此无需为 WS 调大 HTTP server 超时；
//	  2. 鉴权在握手阶段完成（?token= 中的 JWT）——升级成功即绑定 user_id，
//	     之后的长连接不再逐帧鉴权。
func (s *Server) runHTTPServer() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 健康检查（深度检查 db/redis）
	r.GET(constants.HealthCheckPath, func(c *gin.Context) {
		if err := database.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "reason": "db"})
			return
		}
		if err := cache.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "reason": "redis"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// 就绪探针
	r.GET(constants.ReadinessPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	// 版本信息
	r.GET(constants.VersionPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"version": Version})
	})

	// WebSocket：实时推送未读更新。token 取自 ?token=（与 gateway 同源，由前端从 JWT 取），
	// 路径使用 gocommon 契约常量，与 gateway 反向代理保持一致。
	// 链路：浏览器 → Nginx(location /ws/) → Gateway(wsproxy) → 本端口完成升级与鉴权。
	wsHandler := ws.NewHandler(s.hub, s.cfg.JWT.Secret)
	r.GET(constants.WSPathNotification, wsHandler.ServeWS)

	// HTTP 超时（秒）：仅作用于探活等普通 HTTP 请求，升级后的 WS 连接不受影响。
	h := goconfig.Get().Server.HTTP
	s.httpServer = &http.Server{
		Addr:              goconfig.Get().HTTP.Addr(),
		Handler:           r,
		ReadTimeout:       time.Duration(h.ReadTimeoutSec) * time.Second,
		ReadHeaderTimeout: time.Duration(h.ReadHeaderTimeoutSec) * time.Second,
		WriteTimeout:      time.Duration(h.WriteTimeoutSec) * time.Second,
		IdleTimeout:       time.Duration(h.IdleTimeoutSec) * time.Second,
	}
	log.Infof("HTTP server (ws+probe) starting on %s", goconfig.Get().HTTP.Addr())
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Errorf("Failed to start HTTP server: %v", err)
		close(s.quitCh)
	}
}

// runGRPCServer 运行 gRPC 服务器（本服务唯一的业务入口：
// 消息写入/列表/未读数/标记已读，由 Gateway 经反射代理转发）。
func (s *Server) runGRPCServer() {
	lis, err := net.Listen("tcp", goconfig.Get().GRPC.Addr())
	if err != nil {
		log.Errorf("Failed to listen: %v", err)
		close(s.quitCh)
		return
	}

	// gRPC server 选项（keepalive/并发流取自 config.Server.GRPC，仅启动期生效）
	g := goconfig.Get().Server.GRPC
	grpcOpts := []grpc.ServerOption{
		// keepalive 服务端参数：回收空闲/超长连接，防止连接堆积与负载不均
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     time.Duration(g.MaxConnectionIdle) * time.Second,     // 空闲多久后发 GOAWAY
			MaxConnectionAge:      time.Duration(g.MaxConnectionAge) * time.Second,      // 连接最大存活（强制周期重建，实现实例间再均衡）
			MaxConnectionAgeGrace: time.Duration(g.MaxConnectionAgeGrace) * time.Second, // 超龄后给在途 RPC 的宽限期
		}),
		// keepalive 约束：客户端 ping 间隔小于 MinTime 判为滥用（GOAWAY 断连）。
		// 客户端（gateway/grpcclient）的 ping Time 必须大于此值。
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             time.Duration(g.MinPingInterval) * time.Second,
			PermitWithoutStream: true,
		}),
		// 最大并发流（HTTP/2 单连接上的并发 RPC 上限）
		grpc.MaxConcurrentStreams(g.MaxConcurrentStreams),
		// 拦截器链：Panic 恢复（最外层，含指标 panic_counter_total）→ 超时+熔断 → 鉴权 → 指标 → 日志
		grpc.ChainUnaryInterceptor(
			middleware.GRPCRecoveryInterceptor(serviceName),
			middleware.GRPCTimeoutInterceptor(serviceName),
			middleware.GRPCCircuitBreakerInterceptor(s.cb),
			middleware.GRPCAuthInterceptor(),
			middleware.GRPCMetricsInterceptor(serviceName),
			middleware.GRPCLoggingInterceptor(),
		),
	}
	// 链路追踪：服务端基于入站 W3C traceparent 生成子 span。
	// OTel 未初始化时无副作用。
	grpcOpts = append(grpcOpts, observability.GRPCServerOptions()...)

	s.grpcServer = grpc.NewServer(grpcOpts...)
	// 显式注册 gRPC 业务服务：标准 protobuf 生成的 RegisterXxxServiceServer 不会自动生效，
	// 必须在此调用一次，否则客户端调用会报 unknown method。
	// NotificationService 同时实现了 notification.NotificationServiceServer 接口。
	notification.RegisterNotificationServiceServer(s.grpcServer, s.notifySvc)

	// 注册 gRPC 反射服务（Server Reflection）：Gateway 的动态代理不编译任何下游
	// proto 依赖，而是在运行时通过反射协议向本服务查询 proto 描述符（服务/方法/
	// 消息结构），再用 dynamicpb 动态构造请求完成调用。缺少这一行，Gateway 对
	// /api/v1/notification/* 的所有转发都会因查不到描述符而失败。
	reflection.Register(s.grpcServer)

	log.Infof("gRPC server starting on %s", goconfig.Get().GRPC.Addr())
	if err := s.grpcServer.Serve(lis); err != nil {
		log.Errorf("Failed to serve gRPC: %v", err)
		close(s.quitCh)
	}
}

// runMetricsServer 运行指标服务器
func (s *Server) runMetricsServer() {
	addr := fmt.Sprintf(":%d", goconfig.Get().Metrics.Port)
	http.Handle(goconfig.Get().Metrics.Path, promhttp.Handler())

	log.Infof("Metrics server starting on %s%s", addr, goconfig.Get().Metrics.Path)
	if err := http.ListenAndServe(addr, nil); err != nil && err != http.ErrServerClosed {
		log.Errorf("Metrics server error: %v", err)
	}
}

// shutdown 优雅关闭 Server 自身持有的组件：先停 Hub（不再产生新推送），
// 再关 HTTP（等在途请求结束，10s 上限）→ gRPC（GracefulStop 等在途 RPC）。
// DB/Redis 等全局资源由 main 的 releaseInfra 统一释放（与其它服务一致），
// 顺序原则：先摘流量入口，再释放底层资源，避免释放中的资源仍被新请求访问。
func (s *Server) shutdown(ctx context.Context) error {
	if s.hub != nil {
		s.hub.Close()
	}
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	return nil
}

func main() {
	// 顶层兜底：panic 与 run 返回 err 两条路径收敛到同一个出口，
	// 自然走到 defer 统一释放资源（避免中途 log.Fatalf/os.Exit 跳过 defer）。
	var runErr error
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("panic recovered in main: %v\n%s", r, debug.Stack())
			runErr = fmt.Errorf("panic: %v", r)
		}
		if runErr != nil {
			// 用常量而非 goconfig.Get().App.Name：配置加载失败时 Get() 可能为 nil
			log.Errorf("%s exited: %v", serviceName, runErr)
		}
		releaseInfra()
		if runErr != nil {
			os.Exit(1)
		}
	}()

	runErr = run()
}

// run 承载全部初始化与运行逻辑。初始化失败统一返回 error（不再直接 os.Exit），
// 资源释放统一交给 main 的顶层 defer（自然走到 releaseInfra），run 自身不负责释放。
func run() error {
	// ① 加载配置（统一使用 gocommon/config，全局可通过 goconfig.Get() 访问）
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	serviceName = cfg.App.Name

	// ② 初始化日志
	log.Init(cfg.App.LogDir, cfg.App.LogLevel, serviceName)

	// ②.1 启用 Loki 集中日志；未配置时降级为仅本地日志。
	log.EnableLokiFromConfig(cfg.Loki, serviceName)
	// ②.2 启用 OpenTelemetry 链路追踪；未配置时降级为不采集。
	observability.InitAndRegister(serviceName, cfg.OTel)

	// ③ 初始化指标
	metrics.Init(serviceName)
	// 周期性刷新运行时指标（内存/goroutine）并上报服务健康状态，消除 dashboard 长期 0 / No data。
	metricsCtx, metricsCancelFn := context.WithCancel(context.Background())
	metricsCancel = metricsCancelFn
	metrics.StartRuntimeMetrics(metricsCtx, 15*time.Second)
	metrics.StartHealthReporter(metricsCtx, serviceName, 10*time.Second, database.Ping, cache.Ping)

	// ④ 配置中心热更：从 Consul KV 拉取热更配置（限流阈值/日志级别/出站韧性等），
	// 缺失时降级到 config_xxx.yaml 默认值（不致命）。Load 会回写 cfg.RateLimit，
	// 使下方 NewServer 内的 InitRateLimiter 使用热更值；Watch 在后台监听变更即时生效。
	hotCfg = configcenter.Init(cfg.Consul.Address, cfg.App.Name, cfg.App.Env)
	if err := hotCfg.Load(); err != nil && err != configcenter.ErrNotFound {
		log.Warnf("load hot config failed: %v", err)
	}
	go hotCfg.Watch()

	// ⑤ 初始化基础设施（数据库 / Redis）
	db, err := initInfra(cfg)
	if err != nil {
		return err
	}

	// ⑥ 启用 Consul 服务发现（供本服务调用下游时解析实例）
	consul.UseConsulDiscovery(cfg.Consul.Address)

	// ⑦ 注册本服务到 Consul
	deregister, err = registerToConsul(cfg)
	if err != nil {
		return err
	}

	// ⑧ 装配并启动服务（Run 内部监听信号并优雅关闭 HTTP/gRPC/WS）
	server := NewServer(cfg, db)
	if err := server.Run(); err != nil {
		return fmt.Errorf("server error: %v", err)
	}

	// ⑨ 统一释放资源（顺序：先摘流量→关连接→停热更/指标→停日志）
	shutdown()
	return nil
}

// shutdown 正常退出路径释放：先摘流量，再交由 releaseInfra 释放全局资源。
func shutdown() {
	// 1. 先从 Consul 注销，摘除流量（让网关停止转发新请求）
	if deregister != nil {
		if err := deregister(); err != nil {
			log.Warnf("consul deregister: %v", err)
		}
	}
	// 2. 释放其余全局资源（热更/指标/连接池/日志）
	releaseInfra()
}

// releaseInfra 释放所有"已初始化的全局资源"，幂等可重复调用。
// 正常退出由 shutdown 调用；异常退出（panic 兜底、初始化失败 return err 的 defer）也调用，
// 保证无论哪条路径都不会泄漏 Redis/DB 连接、日志句柄或后台指标 goroutine。
func releaseInfra() {
	// 停止配置中心热更监听
	if hotCfg != nil {
		hotCfg.Stop()
	}

	// 取消指标采集上下文，停止后台 goroutine
	if metricsCancel != nil {
		metricsCancel()
	}

	// 释放 Redis 连接池
	if err := cache.Close(); err != nil {
		log.Warnf("cache close: %v", err)
	}

	// 关闭数据库连接池
	if err := database.Close(); err != nil {
		log.Warnf("database close: %v", err)
	}

	// 最后停止链路追踪上报与日志轮转（flush 并关闭日志文件）
	observability.ShutdownGlobal(context.Background())
	log.StopRotation()
}

// loadConfig 通过 gocommon/config 加载配置（已包含 APP_ENV 解析与默认值兜底）。
// 加载失败时返回 error（由调用方统一处理）。
func loadConfig() (*goconfig.Config, error) {
	cfg, err := goconfig.LoadByEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}
	return cfg, nil
}

// registerToConsul 向 Consul 注册本服务实例，返回取消注册函数。
// Version/Canary 供 Gateway 的金丝雀/蓝绿路由策略按版本加权分流
// （同一镜像通过 SERVICE_VERSION / BLOG_CANARY 环境变量区分实例角色）。
// 注册失败视为启动失败，返回 error。
func registerToConsul(cfg *goconfig.Config) (func() error, error) {
	deregister, err := consul.Register(consul.Registration{
		Name:               cfg.App.Name,
		ConsulAddress:      cfg.Consul.Address,
		GRPCPort:           cfg.GRPC.Port,
		HTTPPort:           cfg.HTTP.Port, // HTTP 端口也注册：Gateway 的 WS 反向代理按它寻址
		CheckInterval:      cfg.Consul.CheckInterval,
		DeregisterCritical: cfg.Consul.DeregisterCritical,
		Version:            consul.VersionFromEnv(Version),
		Canary:             consul.CanaryFromEnv(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to register to consul: %v", err)
	}
	return deregister, nil
}
