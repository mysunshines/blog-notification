package ws

import (
	"testing"
	"time"
)

// TestHubPushDeliversToRegisteredClient 验证注册的连接能收到定向推送
func TestHubPushDeliversToRegisteredClient(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Close()

	c := &Client{UserID: 42, conn: nil, send: make(chan []byte, 8)}
	h.Register(c)

	// 等 register 事件被事件循环处理
	time.Sleep(50 * time.Millisecond)

	h.Push(&PushMsg{UserID: 42, Type: "unread", Count: 3})

	select {
	case b := <-c.send:
		if string(b) != `{"type":"unread","count":3}` {
			t.Fatalf("unexpected payload: %s", b)
		}
	case <-time.After(time.Second):
		t.Fatal("push not delivered")
	}
}

// TestHubPushToOfflineUserNoop 目标用户不在线时 Push 不应阻塞/panic
func TestHubPushToOfflineUserNoop(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Close()

	done := make(chan struct{})
	go func() {
		h.Push(&PushMsg{UserID: 999, Type: "unread", Count: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("push to offline user blocked")
	}
}

// TestHubUnregisterClosesSend 验证注销后 send 通道被关闭（writePump 退出依据）
func TestHubUnregisterClosesSend(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Close()

	c := &Client{UserID: 7, conn: nil, send: make(chan []byte, 8)}
	h.Register(c)
	time.Sleep(50 * time.Millisecond)
	h.Unregister(c)

	// 通道关闭后读取应立即得到零值 + ok=false
	deadline := time.After(time.Second)
	for {
		_, ok := <-c.send
		if !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("send channel not closed after unregister")
		default:
		}
	}
}

// TestHubConcurrentRegisterPushUnregister 并发注册/推送/注销，配合 -race 检测数据竞争。
// 每轮使用全新 Client（与真实生命周期一致：一个连接恰好注册/注销一次），
// 避免向已注销（send 已关闭）的连接重复注册造成 send on closed channel。
func TestHubConcurrentRegisterPushUnregister(t *testing.T) {
	h := NewHub()
	go h.Run()
	defer h.Close()

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(uid uint64) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				c := &Client{UserID: uid, conn: nil, send: make(chan []byte, 4)}
				h.Register(c)
				h.Push(&PushMsg{UserID: uid, Type: "unread", Count: int64(j)})
				h.Push(&PushMsg{UserID: uid + 1000, Type: "unread"}) // 推给别的用户
				h.Unregister(c)
			}
		}(uint64(i + 1))
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
