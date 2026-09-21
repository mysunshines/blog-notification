package repository

import (
	"context"
	"fmt"

	"github.com/mysunshines/blog-notification/internal/model"
	"github.com/mysunshines/gocommon/cache"
	"github.com/mysunshines/gocommon/constants"
	svcconst "github.com/mysunshines/blog-notification/internal/constants"

	"gorm.io/gorm"
)

// NotificationRepository 消息仓储
type NotificationRepository struct {
	db *gorm.DB
}

// NewNotificationRepository 构造仓储
func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

// unreadKey 未读计数缓存 key（按 userID 维度）
func unreadKey(userID uint64) string {
	return fmt.Sprintf("%snotification:unread:%d", svcconst.RedisKeyPrefixNotification, userID)
}

// Create 写入一条消息，并递增接收者未读计数。
//
// 一致性说明：DB 与 Redis 非强一致——Redis INCR 失败时消息已落库但计数
// 未加，此时静默返回成功（消息本体不丢），计数误差由 UnreadCount 的
// "缓存缺失回源 DB 重算"路径自愈（TTL 10 分钟或 key 被淘汰后自动校正）。
// 选择最终一致而非分布式事务：未读数是展示型数据，误差可容忍，
// 不值得为它引入两阶段提交的复杂度。
func (r *NotificationRepository) Create(ctx context.Context, n *model.Notification) error {
	if err := r.db.WithContext(ctx).Create(n).Error; err != nil {
		return err
	}
	// 未读计数 +1（Redis 不存在时 Incr 自动从 0 起）
	if _, err := cache.Incr(ctx, unreadKey(n.UserID)); err != nil {
		// 计数失败不阻断消息写入，仅记录（后续读取时以 DB 为准兜底）
		return nil
	}
	_ = cache.Expire(ctx, unreadKey(n.UserID), constants.DefaultCacheTTLNotification)
	return nil
}

// List 分页拉取消息列表（支持 type 过滤 / 仅未读）
func (r *NotificationRepository) List(ctx context.Context, userID uint64, msgType int32, page, pageSize int, onlyUnread bool) ([]model.Notification, int64, error) {
	q := r.db.WithContext(ctx).Model(&model.Notification{}).Where("user_id = ?", userID)
	if msgType > 0 {
		q = q.Where("type = ?", msgType)
	}
	if onlyUnread {
		q = q.Where("is_read = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.Notification
	offset := (page - 1) * pageSize
	if err := q.Order("id DESC").Offset(offset).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// UnreadCount 读取未读计数（优先 Redis，缺失时回源 DB 并回填）
func (r *NotificationRepository) UnreadCount(ctx context.Context, userID uint64) (int64, error) {
	key := unreadKey(userID)
	if s, err := cache.Get(ctx, key); err == nil {
		var n int64
		if _, e := fmt.Sscanf(s, "%d", &n); e == nil {
			return n, nil
		}
	}
	// 回源
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).Count(&n).Error; err != nil {
		return 0, err
	}
	_ = cache.Set(ctx, key, fmt.Sprintf("%d", n), constants.DefaultCacheTTLNotification)
	return n, nil
}

// MarkRead 标记已读（单条或全部），返回操作后的最新未读计数。
//
// 注意 Update 仅命中 is_read=false 的行（RowsAffected 可能小于预期），
// 重复标记不会造成计数漂移；操作后以 DB 重算值覆盖 Redis 缓存，
// 天然修正 Create 阶段可能积累的计数误差。
func (r *NotificationRepository) MarkRead(ctx context.Context, userID, msgID uint64, all bool) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.Notification{}).Where("user_id = ?", userID)
	if !all {
		if msgID == 0 {
			return 0, fmt.Errorf("msg_id or mark_all required")
		}
		q = q.Where("id = ?", msgID)
	}
	// 仅将未读改为已读，避免重复计数
	res := q.Where("is_read = ?", false).Update("is_read", true)
	if res.Error != nil {
		return 0, res.Error
	}
	// 重新统计未读并写回缓存
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).Count(&n).Error; err != nil {
		return 0, err
	}
	_ = cache.Set(ctx, unreadKey(userID), fmt.Sprintf("%d", n), constants.DefaultCacheTTLNotification)
	return n, nil
}
