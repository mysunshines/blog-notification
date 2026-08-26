package model

import (
	"time"

	notification "github.com/mysunshines/blog-notification/proto/pb"
)

// Notification 站内消息持久化模型
type Notification struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint64    `gorm:"index:idx_user_unread;not null" json:"user_id"`
	Type      int32     `gorm:"index;not null" json:"type"` // 对应 notification.NotificationType
	Title     string    `gorm:"type:varchar(255);not null" json:"title"`
	Content   string    `gorm:"type:varchar(1024)" json:"content"`
	Link      string    `gorm:"type:varchar(512)" json:"link"`
	ActorID   uint64    `gorm:"not null;default:0" json:"actor_id"`
	ActorName string    `gorm:"type:varchar(64)" json:"actor_name"`
	IsRead    bool      `gorm:"index:idx_user_unread;not null;default:0" json:"is_read"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
}

// TableName 指定表名
func (Notification) TableName() string {
	return "notifications"
}

// ToProto 转换为 proto 实体
func (n *Notification) ToProto() *notification.NotificationMessage {
	return &notification.NotificationMessage{
		Id:        n.ID,
		UserId:    n.UserID,
		Type:      notification.NotificationType(n.Type),
		Title:     n.Title,
		Content:   n.Content,
		Link:      n.Link,
		IsRead:    n.IsRead,
		CreatedAt: n.CreatedAt.Unix(),
	}
}
