package constants

// RedisKeyPrefixNotification 站内消息服务 Redis Key 前缀（服务内部实现细节，下沉至此，
// 不再依赖 gocommon；gocommon/cache.GetKey 会自动拼接该前缀）。
const RedisKeyPrefixNotification = "notification:"
