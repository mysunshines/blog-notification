# Notification Service - 站内消息服务

## 一、服务概述

站内消息服务负责消息的写入、拉取、未读计数与实时推送。上游是 article-service（审核通过/驳回、下线、文章点赞）与 comment-service（文章评论、评论回复、评论点赞），通过 gRPC 写入消息；下游消费方是 Web 前端——REST 拉取列表/计数，WebSocket 接收实时未读推送。

**端口配置**:

| 端口 | 用途 |
|------|------|
| 9105 | gRPC（业务入口：CreateMessage / GetMessages / GetUnreadCount / MarkRead） |
| 8085 | HTTP（WebSocket `/ws/notification` + 探活 `/health` `/ready` `/version`） |
| 9099 | Prometheus Metrics |

## 二、技术栈

| 类别 | 技术 |
|------|------|
| 语言 | Go 1.25+ |
| RPC框架 | gRPC + Protobuf（**业务层纯 gRPC**；HTTP 仅承载 WS 升级与探活） |
| 实时推送 | gorilla/websocket（Hub 按 userID 分组定向推送） |
| 数据库 | MySQL 8.0（独立库 `notification_db`） |
| 缓存 | Redis（未读计数，最终一致） |
| 监控 | Prometheus |
| 注册中心 | Consul（gRPC + HTTP 双端口注册） |
| 熔断器 | gobreaker |
| JWT | golang-jwt/jwt/v5（gRPC 鉴权 + WS 握手共用密钥） |

## 三、项目结构

```
notification-service/
├── cmd/server/
│   └── main.go                       # 启动：基础设施初始化、Consul 注册、三组监听、优雅退出
├── internal/
│   ├── model/notification.go         # GORM 模型（notifications 表）+ proto 转换
│   ├── repository/
│   │   └── notification_repository.go # 数据访问：CRUD + Redis 未读计数（缓存缺失回源）
│   ├── service/
│   │   └── notification_service.go   # gRPC handler：鉴权（JWT 取接收者）、写入后推送
│   └── ws/
│       ├── hub.go                    # 连接管理：按 userID 分组、事件循环、非阻塞推送
│       ├── hub_test.go               # 并发测试（-race 覆盖注册/推送/注销竞态）
│       └── handler.go                # WS 升级、JWT 校验、read/writePump（ping/pong 心跳）
├── proto/
│   ├── notification.proto            # 服务契约（单一来源，pb 产物提交入库）
│   └── pb/                           # protoc 生成代码
├── config/
│   ├── config.yaml                   # 本地开发
│   └── config_test.yaml              # Docker test 环境（基础设施容器地址）
├── Dockerfile
└── Makefile
```

## 四、API 列表

### 4.1 gRPC API（`notification.v1.NotificationService`）

前端经网关反射代理访问，路径 `/api/v1/notification/<snake_method>`。**网关会把路径末段转 PascalCase 匹配 gRPC 方法名，前端必须写完整 snake_case 方法名**（如 `get_messages`），写短名会反射失败并触发熔断。

| 方法 | 路径（经网关） | 描述 | 认证 |
|------|---------------|------|------|
| CreateMessage | POST `/api/v1/notification/create_message` | 写入消息（article/comment 服务调用） | 服务间调用（AuthForward 自动透传 token） |
| GetMessages | GET `/api/v1/notification/get_messages` | 分页拉取消息列表（支持 type 过滤、仅未读） | JWT（接收者从 JWT 提取，防 IDOR） |
| GetUnreadCount | GET `/api/v1/notification/get_unread_count` | 未读计数 | JWT |
| MarkRead | POST `/api/v1/notification/mark_read` | 标记已读（单条或全部） | JWT |

#### GetMessages 查询参数

```
page: int,          // 选填, 页码, 默认 1
page_size: int,     // 选填, 每页数量, 默认 20, 上限 100
type: int,          // 选填, 消息类型过滤（0=全部）
only_unread: bool   // 选填, 仅返回未读
```

#### MarkRead 请求体

```json
{
    "message_id": 42,     // uint, 与 mark_all 二选一
    "mark_all": false     // bool, true 时标记全部已读
}
```

#### 消息类型（NotificationType）

| 值 | 枚举 | 触发方 |
|----|------|--------|
| 1 | ARTICLE_APPROVED | article-service（管理员审核通过） |
| 2 | ARTICLE_REJECTED | article-service（审核驳回，content 携带原因） |
| 3 | ARTICLE_OFFLINED | article-service（下线，content 携带原因） |
| 4 | ARTICLE_LIKED | article-service（文章被点赞） |
| 5 | ARTICLE_COMMENTED | comment-service（文章被评论，content 携带评论摘要） |
| 6 | COMMENT_LIKED | comment-service（评论被点赞） |
| 7 | COMMENT_REPLIED | comment-service（评论被回复，content 携带回复摘要） |

### 4.2 WebSocket API

**连接地址**: `ws(s)://<host>/ws/notification?token=<JWT>`

链路：浏览器 → Nginx（`location /ws/`，Upgrade 头透传）→ Gateway（wsproxy，Consul 寻址反向代理）→ 本服务 8085 端口完成 WS 升级并校验 JWT。

**推送消息格式**（服务端 → 客户端，JSON 文本帧）：

```json
{
    "type": "unread",          // 当前只有 "unread"（未读数更新）
    "count": 3,                // 最新未读总数
    "data": {                  // 可选：触发本次推送的消息实体
        "message": { "id": 42, "title": "...", "link": "/article.html?..." }
    }
}
```

**心跳约定**（三层，缺一不可）：

| 层 | 机制 | 参数 |
|----|------|------|
| 浏览器 ↔ Gateway | 前端 25s 文本心跳（`ws.send('ping')`） | Nginx 读超时 300s |
| Gateway ↔ 本服务 | WS 控制帧透明转发 | 同上 |
| 本服务内部 | writePump 30s Ping 控制帧，浏览器自动回 Pong | readPump 读超时 60s |

> 浏览器 WebSocket API **无法发送 ping 控制帧**（只能被动响应服务端 ping），因此前端保活必须用文本心跳；服务端 readPump 收到任意消息（含文本心跳）都会刷新读超时。

## 五、核心数据流

```
写入链路（best-effort，失败不影响主业务）：
  article/comment 服务
    → grpcclient.SendRequest("/notification.v1.NotificationService/CreateMessage")
    → 校验 user_id/type
    → DB INSERT notifications
    → Redis INCR notification:unread:<uid>（失败不阻断，误差可自愈）
    → hub.Push（非阻塞；用户在线则 WS 推送最新未读数）

读取链路：
  前端 GET /api/v1/notification/get_messages
    → Gateway 反射代理 → gRPC GetMessages
    → JWT 提取接收者 uid（忽略请求体 user_id，防 IDOR）
    → DB 分页查询（idx_user_unread 索引）
    → 未读数：Redis 优先，缺失回源 DB COUNT 并回填缓存

实时推送链路：
  前端 WS 连接（Nginx → Gateway wsproxy → 本服务）
    → JWT 校验 → hub.Register（按 userID 分组）
    → CreateMessage/MarkRead 后 hub.Push 推送 {type:"unread", count}
    → 消息缓冲满时丢弃该条推送（前端拉取接口兜底，不阻塞业务 gRPC）
```

## 六、数据库模型

### notifications 表（库：notification_db）

| 字段 | 类型 | 约束 | 描述 |
|------|------|------|------|
| id | BIGINT UNSIGNED | PRIMARY KEY, AUTO_INCREMENT | 消息ID |
| user_id | BIGINT UNSIGNED | NOT NULL, 复合索引 | 接收者ID |
| type | INT | NOT NULL, INDEX | 消息类型（对应 NotificationType） |
| title | VARCHAR(255) | NOT NULL | 标题（如「你的文章《xx》已通过审核」） |
| content | VARCHAR(1024) | DEFAULT '' | 正文摘要（评论内容、驳回原因等） |
| link | VARCHAR(512) | DEFAULT '' | 前端跳转链接（相对路径） |
| actor_id | BIGINT UNSIGNED | DEFAULT 0 | 触发者ID（0=系统） |
| actor_name | VARCHAR(64) | DEFAULT '' | 触发者昵称（展示用快照） |
| is_read | TINYINT UNSIGNED | DEFAULT 0 | 已读标记 |
| created_at | TIMESTAMP | DEFAULT CURRENT_TIMESTAMP | 创建时间 |

**索引设计**:

| 索引 | 列 | 覆盖场景 |
|------|-----|---------|
| idx_notifications_user_unread | (user_id, is_read) | 未读列表（only_unread）、未读 COUNT、MarkRead 更新 |
| idx_notifications_type | (type) | 类型过滤 |
| idx_notifications_created | (created_at) | 时间排序/归档 |

建表语句见 `infra/scripts/init.sql`（库与表初始化的单一来源）。

## 七、高并发与可靠性设计

### 7.1 Hub 连接管理（internal/ws/hub.go）

- **分组结构**：`map[userID]map[*Client]struct{}` 支持同一用户多标签页/多设备同时在线，推送时全部送达
- **并发安全**：注册/注销/推送统一走事件循环 + RWMutex；推送在 RLock 内完成非阻塞投递（select+default），不复制 set —— 复制期间连接可能被注销，向已关闭 channel 发送会 panic
- **背压策略**：client.send 缓冲 64 条；hub.notify 缓冲 256 条。两级缓冲满则丢弃推送并告警，**绝不反压业务 gRPC handler** —— 未读数以 DB/Redis 为准，客户端可随时拉取兜底
- **幂等关闭**：closeOnce 保证 Close() 可安全多次调用；send channel 的关闭由 Unregister 事件唯一负责（writePump 据此退出）

### 7.2 未读计数的最终一致

```
正常路径：  Create → Redis INCR → 过期 10min
回源路径：  UnreadCount 缓存 miss → DB COUNT(*) WHERE user_id AND is_read=0 → 回填
自愈路径：  MarkRead 后无条件 DB 重算并 SET 覆盖缓存（修正历史误差）
```

不使用分布式事务的原因：未读数是展示型数据，秒级误差可容忍；消息本体始终以 DB 为准不丢失。

### 7.3 鉴权模型（防 IDOR）

| 方法 | 身份来源 | 说明 |
|------|---------|------|
| GetMessages / GetUnreadCount / MarkRead | gRPC context（`middleware.RequireGRPCAuth` 从 JWT 提取） | 请求体的 user_id 字段被忽略，无法读取/操作他人消息 |
| CreateMessage | 信任调用方 | 供内网服务间调用（与其它服务写方法一致的信任模型；如需彻底防伪造需引入服务间凭证） |
| WS 握手 | `?token=` 中的 JWT | 校验签名后提取 user_id 绑定连接 |

### 7.4 gRPC 拦截器链

```
grpcUnaryInterceptor（超时 + gobreaker 熔断）
  → GRPCAuthInterceptor    # 校验 metadata Authorization，注入身份到 ctx
  → GRPCMetricsInterceptor # Prometheus 指标
  → GRPCLoggingInterceptor # 结构化日志
```

超时按方法分级（`middleware.GRPCMethodTimeout`），熔断参数可由 Consul 配置中心热更（`resilience` 配置段）。

### 7.5 心跳与断线恢复

服务端 30s Ping + 前端 25s 文本心跳双层保活（见 4.2 节心跳表）；前端断线后指数退避重连（1s→2s→…→30s 封顶），重连成功与页面重新可见时都兜底拉一次未读数，防止断连期间漏推。

## 八、部署

### 8.1 Docker（docker-compose.yml 已含本服务）

```yaml
notification-service:
  build: ./notification-service
  ports:
    - "8085:8085"   # WS + 探活（Gateway wsproxy 的代理目标）
    - "9105:9105"   # gRPC
```

前置条件：

1. `infra/scripts/init.sql` 已执行（创建 notification_db 库与 notifications 表）
2. Gateway 依赖已加 notification-service（compose depends_on 已配置）
3. Nginx `location /ws/` 已配置 Upgrade 头透传（infra/nginx/nginx.conf）

### 8.2 本地开发

```bash
make deps      # 安装依赖
make run       # 直接运行（读 config/config.yaml）
make test      # 单元测试
go test -race ./internal/ws/   # 并发测试（hub 竞态检测）
```

### 8.3 上游服务接入

article/comment 服务通过 `replace` 指令引用本模块的 proto：

```go
// go.mod: replace github.com/mysunshines/blog-notification => ../notification-service

import notification "github.com/mysunshines/blog-notification/proto/pb"

var resp notification.CreateMessageResponse
err := grpcclient.SendRequest(ctx,
    notification.NotificationService_CreateMessage_FullMethodName,
    &notification.CreateMessageRequest{...}, &resp)
```

**约定**：通知是 best-effort —— 调用失败仅打日志、绝不影响主业务；自己触发自己的动作（自评/自赞/自回）不发送通知；正文超长由调用方按 rune 截断（content 上限 1024 字节）。

## 九、Prometheus Metrics

| 指标名称 | 类型 | 标签 | 描述 |
|----------|------|------|------|
| `http_requests_total` | Counter | method, endpoint, status | HTTP请求总数 |
| `rpc_requests_total` | Counter | service, method, status | gRPC请求总数 |
| `rpc_request_duration_seconds` | Histogram | service, method | gRPC请求延迟 |
| `requests_in_flight` | Gauge | - | 当前处理中的请求数 |
| `goroutine_count` | Gauge | - | Goroutine数量 |
| `memory_usage_bytes` | Gauge | - | 内存使用量 |
| `panic_counter_total` | Counter | service | Panic次数 |
| `mysql_slow_queries_total` | Counter | - | MySQL慢查询数 |
| `cache_operations_total` | Counter | operation, status | Redis缓存操作数 |
| `service_health` | Gauge | service | 依赖健康状态（DB/Redis Ping） |

## 十、进程退出与资源释放

监听 `SIGINT`/`SIGTERM` 后按序释放：

1. **停推送**：hub.Close() 停止事件循环（先于 HTTP 关闭，不再产生新推送）
2. **摘流量**：HTTP Shutdown（10s 上限等在途请求）→ gRPC GracefulStop
3. **释放连接**：MySQL 连接池 → Redis 连接池
4. **收尾**：Consul 注销 → 配置中心监听停止 → 指标采集取消 → 日志 flush

异常退出兜底：初始化失败与运行期 panic 均走 `releaseInfra()` 幂等释放（不 `os.Exit` 泄漏资源）；任一 server goroutine 失败通过 `quitCh` 触发整体优雅关闭，避免半死状态。
