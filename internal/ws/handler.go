package ws

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/mysunshines/gocommon/constants"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true }, // 同源经 gateway，允许
}

// Handler WebSocket 处理器
type Handler struct {
	hub       *Hub
	jwtSecret string
}

// NewHandler 构造 WS handler
func NewHandler(hub *Hub, jwtSecret string) *Handler {
	return &Handler{hub: hub, jwtSecret: jwtSecret}
}

// ServeWS 处理 /ws/notification?token=xxx 连接
func (h *Handler) ServeWS(c *gin.Context) {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(h.jwtSecret), nil
	}); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	uid, ok := claims[constants.JWTClaimUserID].(float64)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid claims"})
		return
	}
	userID := uint64(uid)

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		zap.L().Error("ws upgrade failed", zap.Error(err))
		return
	}
	client := &Client{UserID: userID, conn: conn, send: make(chan []byte, 64)}
	h.hub.Register(client)

	go h.writePump(client)
	go h.readPump(client)
}

// readPump 读取客户端消息（心跳/关闭），此处忽略业务消息。
// 读超时 60s 由 writePump 的 30s ping 间隔 + 浏览器自动回 pong 维持：
// 浏览器端 WebSocket API 无法主动发送 ping 帧，只能被动响应服务端 ping，
// 因此保活必须由服务端发起。
func (h *Handler) readPump(client *Client) {
	defer func() {
		h.hub.Unregister(client)
		_ = client.conn.Close()
	}()
	client.conn.SetReadLimit(1 << 10)
	_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
	client.conn.SetPongHandler(func(string) error {
		_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := client.conn.ReadMessage(); err != nil {
			return
		}
		// 收到任意消息（含前端文本心跳）也刷新读超时：
		// 浏览器无法发送 ping 控制帧，前端通过周期性文本心跳保活。
		_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
	}
}

// 心跳参数：ping 间隔需显著小于 pongWait，为网络往返与 pong 丢失留余量。
const (
	pingPeriod = 30 * time.Second
	pongWait   = 60 * time.Second
)

// writePump 将推送消息写入连接，并按 pingPeriod 发送 ping 维持心跳。
func (h *Handler) writePump(client *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = client.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-client.send:
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// hub 已注销该连接（send 被关闭），发送关闭帧后退出
				_ = client.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := client.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
