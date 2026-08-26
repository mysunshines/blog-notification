package service

import (
	"context"

	notification "github.com/mysunshines/blog-notification/proto/pb"
	"github.com/mysunshines/blog-notification/internal/model"
	"github.com/mysunshines/blog-notification/internal/repository"
	"github.com/mysunshines/blog-notification/internal/ws"

	"github.com/mysunshines/gocommon/middleware"
	"go.uber.org/zap"
)

// NotificationService 实现 proto 定义的 NotificationServiceServer
type NotificationService struct {
	notification.UnimplementedNotificationServiceServer
	repo *repository.NotificationRepository
	hub  *ws.Hub
}

// NewNotificationService 构造服务
func NewNotificationService(repo *repository.NotificationRepository, hub *ws.Hub) *NotificationService {
	return &NotificationService{repo: repo, hub: hub}
}

// CreateMessage 写入消息并实时推送未读更新。
// 仅供 article/comment 等业务服务经内网 gRPC 调用（gateway 反射代理同样可达，
// 属已知信任边界内的写入口），与其它服务的写方法保持一致的鉴权模型。
func (s *NotificationService) CreateMessage(ctx context.Context, req *notification.CreateMessageRequest) (*notification.CreateMessageResponse, error) {
	if req.UserId == 0 {
		return &notification.CreateMessageResponse{Code: 400, Message: "user_id required"}, nil
	}
	if req.Type == notification.NotificationType_NOTIFICATION_TYPE_UNSPECIFIED {
		return &notification.CreateMessageResponse{Code: 400, Message: "type required"}, nil
	}
	n := &model.Notification{
		UserID:    req.UserId,
		Type:      int32(req.Type),
		Title:     req.Title,
		Content:   req.Content,
		Link:      req.Link,
		ActorID:   req.ActorId,
		ActorName: req.ActorName,
	}
	if err := s.repo.Create(ctx, n); err != nil {
		zap.L().Error("create notification failed", zap.Error(err))
		return &notification.CreateMessageResponse{Code: 500, Message: "internal error"}, nil
	}
	// 推送未读更新（best-effort）
	if cnt, err := s.repo.UnreadCount(ctx, n.UserID); err == nil {
		s.hub.Push(&ws.PushMsg{
			UserID: n.UserID,
			Type:   "unread",
			Count:  cnt,
			Data: map[string]interface{}{
				"message": n.ToProto(),
			},
		})
	}
	return &notification.CreateMessageResponse{
		Code:    0,
		Message: "ok",
		Data:    n.ToProto(),
	}, nil
}

// GetMessages 拉取消息列表（接收者从 JWT 提取，忽略请求体 user_id，防 IDOR）
func (s *NotificationService) GetMessages(ctx context.Context, req *notification.GetMessagesRequest) (*notification.GetMessagesResponse, error) {
	uid, err := middleware.RequireGRPCAuth(ctx)
	if err != nil {
		return &notification.GetMessagesResponse{Code: 401, Message: "authentication required"}, nil
	}
	page, size := int(req.Page), int(req.PageSize)
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	list, total, err := s.repo.List(ctx, uint64(uid), int32(req.Type), page, size, req.OnlyUnread)
	if err != nil {
		zap.L().Error("list notifications failed", zap.Error(err))
		return &notification.GetMessagesResponse{Code: 500, Message: "internal error"}, nil
	}
	unread, err := s.repo.UnreadCount(ctx, uint64(uid))
	if err != nil {
		zap.L().Warn("get unread count failed", zap.Error(err))
	}
	msgs := make([]*notification.NotificationMessage, 0, len(list))
	for i := range list {
		msgs = append(msgs, list[i].ToProto())
	}
	return &notification.GetMessagesResponse{
		Code:         0,
		Message:      "ok",
		List:         msgs,
		Total:        total,
		UnreadCount:  unread,
	}, nil
}

// GetUnreadCount 未读计数（接收者从 JWT 提取，防 IDOR）
func (s *NotificationService) GetUnreadCount(ctx context.Context, req *notification.GetUnreadCountRequest) (*notification.GetUnreadCountResponse, error) {
	uid, err := middleware.RequireGRPCAuth(ctx)
	if err != nil {
		return &notification.GetUnreadCountResponse{Code: 401, Message: "authentication required"}, nil
	}
	cnt, err := s.repo.UnreadCount(ctx, uint64(uid))
	if err != nil {
		zap.L().Error("get unread count failed", zap.Error(err))
		return &notification.GetUnreadCountResponse{Code: 500, Message: "internal error"}, nil
	}
	return &notification.GetUnreadCountResponse{Code: 0, Message: "ok", UnreadCount: cnt}, nil
}

// MarkRead 标记已读（接收者从 JWT 提取，防 IDOR）
func (s *NotificationService) MarkRead(ctx context.Context, req *notification.MarkReadRequest) (*notification.MarkReadResponse, error) {
	uid, err := middleware.RequireGRPCAuth(ctx)
	if err != nil {
		return &notification.MarkReadResponse{Code: 401, Message: "authentication required"}, nil
	}
	if req.MessageId == 0 && !req.MarkAll {
		return &notification.MarkReadResponse{Code: 400, Message: "message_id or mark_all required"}, nil
	}
	cnt, err := s.repo.MarkRead(ctx, uint64(uid), req.MessageId, req.MarkAll)
	if err != nil {
		zap.L().Error("mark read failed", zap.Error(err))
		return &notification.MarkReadResponse{Code: 500, Message: "internal error"}, nil
	}
	// 推送最新未读数，联动其它已打开的标签页/设备
	s.hub.Push(&ws.PushMsg{UserID: uint64(uid), Type: "unread", Count: cnt})
	return &notification.MarkReadResponse{Code: 0, Message: "ok", UnreadCount: cnt}, nil
}
