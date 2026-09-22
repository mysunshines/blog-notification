# notification-service 对外 API 文档

> 自动生成自 `notification.proto`（模式：proto）。
> 网关按 `/api/v1/notification/<snake_method>` 反射代理到 gRPC 方法 `notification.v1.NotificationService/<Method>`。
> 生成时间：2026-09-21 19:37:19
> Base URL（网关入口）：http://localhost:8081

## 接口列表

| Method | Path | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/api/v1/notification/create_message` | 公开 | 写入消息（article/comment 等业务服务调用） |
| `GET` | `/api/v1/notification/get_messages` | 公开 | 拉取消息列表 |
| `GET` | `/api/v1/notification/get_unread_count` | 公开 | 未读计数 |
| `POST` | `/api/v1/notification/mark_read` | 公开 | 标记已读 |

## CreateMessage

- **URL**: `http://localhost:8081/api/v1/notification/create_message`
- **Method**: `POST`
- **鉴权**: 公开（无需鉴权）

### Headers
```http
Content-Type: application/json
```

### Request
**参数位置**：Request Body（JSON）

| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `user_id` | `uint64` | 消息接收者（必填） | `0` |
| `type` | `NotificationType` | 消息类型（必填） | `ARTICLE_APPROVED` |
| `title` | `string` |  | `""` |
| `content` | `string` |  | `""` |
| `link` | `string` |  | `""` |
| `actor_id` | `uint64` |  | `0` |
| `actor_name` | `string` |  | `""` |

**Body 示例**：
```json
{"user_id": 0, "type": {}, "title": "", "content": "", "link": "", "actor_id": 0, "actor_name": ""}
```

### Response
| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `code` | `int32` |  | `0` |
| `message` | `string` |  | `""` |
| `data` | `NotificationMessage` |  | 见 [NotificationMessage](#notificationmessage) |

**Response 示例**：
```json
{"code": 0, "message": "success", "data": {}}
```

### curl 示例
```bash
curl -X POST 'http://localhost:8081/api/v1/notification/create_message' \
  -H 'Content-Type: application/json' \
  -d '{"user_id": 0, "type": {}, "title": "", "content": "", "link": "", "actor_id": 0, "actor_name": ""}'
```

## GetMessages

- **URL**: `http://localhost:8081/api/v1/notification/get_messages?user_id=0&type=ARTICLE_APPROVED&page=0&page_size=0&only_unread=false`
- **Method**: `GET`
- **鉴权**: 公开（无需鉴权）

### Headers
```http
Content-Type: application/json
```

### Request
**参数位置**：Query String

| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `user_id` | `uint64` |  | `0` |
| `type` | `NotificationType` |  | `ARTICLE_APPROVED` |
| `page` | `int32` |  | `0` |
| `page_size` | `int32` |  | `0` |
| `only_unread` | `bool` |  | `false` |

**Query 示例**：
```json
user_id=0&type=ARTICLE_APPROVED&page=0&page_size=0&only_unread=false
```

### Response
| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `code` | `int32` |  | `0` |
| `message` | `string` |  | `""` |
| `list` | `NotificationMessage[]` |  | [] |
| `total` | `int64` |  | `0` |
| `unread_count` | `int64` |  | `0` |

**Response 示例**：
```json
{"code": 0, "message": "success", "list": [], "total": 0, "unread_count": 0}
```

### curl 示例
```bash
curl -X GET 'http://localhost:8081/api/v1/notification/get_messages?user_id=0&type=ARTICLE_APPROVED&page=0&page_size=0&only_unread=false'
```

## GetUnreadCount

- **URL**: `http://localhost:8081/api/v1/notification/get_unread_count?user_id=0`
- **Method**: `GET`
- **鉴权**: 公开（无需鉴权）

### Headers
```http
Content-Type: application/json
```

### Request
**参数位置**：Query String

| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `user_id` | `uint64` |  | `0` |

**Query 示例**：
```json
user_id=0
```

### Response
| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `code` | `int32` |  | `0` |
| `message` | `string` |  | `""` |
| `unread_count` | `int64` |  | `0` |

**Response 示例**：
```json
{"code": 0, "message": "success", "unread_count": 0}
```

### curl 示例
```bash
curl -X GET 'http://localhost:8081/api/v1/notification/get_unread_count?user_id=0'
```

## MarkRead

- **URL**: `http://localhost:8081/api/v1/notification/mark_read`
- **Method**: `POST`
- **鉴权**: 公开（无需鉴权）

### Headers
```http
Content-Type: application/json
```

### Request
**参数位置**：Request Body（JSON）

| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `user_id` | `uint64` |  | `0` |
| `message_id` | `uint64` |  | `0` |
| `mark_all` | `bool` |  | `false` |

**Body 示例**：
```json
{"user_id": 0, "message_id": 0, "mark_all": false}
```

### Response
| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `code` | `int32` |  | `0` |
| `message` | `string` |  | `""` |
| `unread_count` | `int64` |  | `0` |

**Response 示例**：
```json
{"code": 0, "message": "success", "unread_count": 0}
```

### curl 示例
```bash
curl -X POST 'http://localhost:8081/api/v1/notification/mark_read' \
  -H 'Content-Type: application/json' \
  -d '{"user_id": 0, "message_id": 0, "mark_all": false}'
```

---

## 数据结构

> 下列 message / enum 被上述接口的请求或响应引用；结构体字段中的 message 类型可点击跳转到对应定义。

### NotificationType (enum)

| 值 | 编号 | 说明 |
| --- | --- | --- |
| `NOTIFICATION_TYPE_UNSPECIFIED` | `0` |  |
| `ARTICLE_APPROVED` | `1` |  |
| `ARTICLE_REJECTED` | `2` |  |
| `ARTICLE_OFFLINED` | `3` |  |
| `ARTICLE_LIKED` | `4` |  |
| `ARTICLE_COMMENTED` | `5` |  |
| `COMMENT_LIKED` | `6` |  |
| `COMMENT_REPLIED` | `7` |  |

### NotificationMessage

| 字段 | 类型 | 说明 | 示例 |
| --- | --- | --- | --- |
| `id` | `uint64` |  | `0` |
| `user_id` | `uint64` | 消息接收者 | `0` |
| `type` | `NotificationType` | 消息类型 | `ARTICLE_APPROVED` |
| `title` | `string` | 标题（如「你的文章《xxx》已通过审核」） | `""` |
| `content` | `string` | 正文（如评论内容；审核类可为空） | `""` |
| `link` | `string` | 跳转链接（相对路径，如 /article/123） | `""` |
| `is_read` | `bool` | 是否已读 | `false` |
| `created_at` | `int64` | 创建时间戳（秒） | `0` |

