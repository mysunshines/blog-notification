package ws

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// Client 表示一个已认证的 WebSocket 连接
type Client struct {
	UserID uint64
	conn   *websocket.Conn
	send   chan []byte
}

// Hub 维护所有在线连接，按 userID 分组，用于定向推送未读更新。
type Hub struct {
	mu       sync.RWMutex
	clients  map[uint64]map[*Client]struct{}
	register chan *Client
	unreg    chan *Client
	notify   chan *PushMsg
	done     chan struct{}
	// closeOnce 保证 Close 幂等（重复调用不会 panic 于 double close）
	closeOnce sync.Once
}

// PushMsg 推送消息体
type PushMsg struct {
	UserID uint64                 `json:"-"`
	Type   string                 `json:"type"` // "unread" | "message"
	Count  int64                  `json:"count,omitempty"`
	Data   map[string]interface{} `json:"data,omitempty"`
}

// NewHub 构造 Hub
func NewHub() *Hub {
	return &Hub{
		clients:  make(map[uint64]map[*Client]struct{}),
		register: make(chan *Client, 64),
		unreg:    make(chan *Client, 64),
		notify:   make(chan *PushMsg, 256),
		done:     make(chan struct{}),
	}
}

// Run 启动 hub 事件循环，直到 Close 被调用。
func (h *Hub) Run() {
	for {
		select {
		case <-h.done:
			return
		case c := <-h.register:
			h.mu.Lock()
			if h.clients[c.UserID] == nil {
				h.clients[c.UserID] = make(map[*Client]struct{})
			}
			h.clients[c.UserID][c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unreg:
			h.mu.Lock()
			if set, ok := h.clients[c.UserID]; ok {
				if _, exists := set[c]; exists {
					delete(set, c)
					close(c.send)
				}
				if len(set) == 0 {
					delete(h.clients, c.UserID)
				}
			}
			h.mu.Unlock()
		case m := <-h.notify:
			b, err := json.Marshal(m)
			if err != nil {
				zap.L().Error("ws marshal push msg failed", zap.Error(err))
				continue
			}
			// 发送是非阻塞的（select default），持读锁遍历开销极小；
			// 若先复制 set 再释放锁，复制期间连接可能已被注销，反而向已关闭的
			// send channel 写入造成 panic，因此直接在锁内完成投递。
			h.mu.RLock()
			for c := range h.clients[m.UserID] {
				select {
				case c.send <- b:
				default:
					// 发送缓冲区满，跳过该连接（客户端会通过拉取接口兜底）
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Register 注册连接
func (h *Hub) Register(c *Client) {
	select {
	case h.register <- c:
	case <-h.done:
	}
}

// Unregister 注销连接
func (h *Hub) Unregister(c *Client) {
	select {
	case h.unreg <- c:
	case <-h.done:
	}
}

// Push 推送消息给指定用户。
// 非阻塞：缓冲区满时丢弃该条推送并告警——未读数以 DB/Redis 为准，
// 客户端可通过拉取接口兜底，不应让推送反压阻塞业务 gRPC handler。
func (h *Hub) Push(m *PushMsg) {
	// 先检查 hub 是否已停止，再做非阻塞投递：
	// 若直接写进同一个带 default 的 select，done 关闭且缓冲区有空位时，
	// Go 会随机选中 default 分支，导致无谓丢弃消息。
	select {
	case <-h.done:
		return
	default:
	}
	select {
	case h.notify <- m:
	default:
		zap.L().Warn("ws notify channel full, push dropped", zap.Uint64("user_id", m.UserID))
	}
}

// Close 停止事件循环（幂等，可安全多次调用）。
func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		close(h.done)
	})
}
