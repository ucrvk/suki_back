# API 文档

本文档只描述 Gin 暴露的 HTTP 接口。

## 通用约定

- 所有接口默认返回 JSON，图片接口除外。
- 除 `/pic-proxy/*target` 外，CORS 为宽松策略。
- 默认监听地址为 `:6988`。
- 如果监听地址带路径前缀，例如 `:6988/api`，则所有路由会挂载到该前缀下。

## 接口列表

### `GET /manifest.json`

返回当前 `vrcid -> image_hash` 映射表。

#### 响应示例

```json
{
  "maid_1776839632169": "655daw2"
}
```

#### 状态码

- `200 OK`
- `500 Internal Server Error`

---

### `GET /images/:file`

返回本地缓存的 AVIF 图片文件。

#### 路径参数

- `file`：文件名，必须以 `.avif` 结尾。

#### 说明

- 响应头包含长缓存策略：
  - `Cache-Control: public, max-age=31536000, s-maxage=31536000, immutable`

#### 状态码

- `200 OK`
- `400 Bad Request`
- `404 Not Found`

---

### `GET /pic-proxy/*target`

拉取远程图片，按最长边最多 `900px` 等比缩放并转为 AVIF 后返回，同时写入本地磁盘缓存。

#### 路径参数

- `target`：完整的 `https://...` 远程图片地址
- 目标地址通过 wildcard 接收，包含特殊字符时建议 URL 编码

#### 支持范围

- 只接受 `https://...` 远程地址
- 远端响应 `Content-Type` 仅允许：
  - `image/png`
  - `image/jpeg`
  - `image/jpg`
  - `image/webp`
  - `application/octet-stream`，且 URL 路径必须以 `.jpg`、`.png`、`.webp` 结尾
- 若原图最长边大于 `900px`，则按比例缩放到最长边 `900px`
- 若原图最长边不超过 `900px`，则保持原尺寸
- 输出统一为 AVIF
- 缓存键按归一化后的目标 URL 生成
- 磁盘缓存最多保留 `1000` 张，按 LRU 删除旧文件

#### 响应头

- `Content-Type: image/avif`
- `Cache-Control: public, max-age=31536000, immutable`
- CORS 仅允许以下来源：
  - `https://suki.wenwen12305.top`
  - `http://localhost:*`
  - `http://127.0.0.1:*`

#### 状态码

- `200 OK`
- `400 Bad Request`
- `415 Unsupported Media Type`
- `500 Internal Server Error`
- `502 Bad Gateway`

---

### `POST /subscription`

将指定设备 token 订阅到 `booking_open` topic。

#### 请求体

```json
{
  "token": "fcm_device_token"
}
```

#### 响应示例

```json
{
  "topic": "booking_open",
  "success_count": 1,
  "failure_count": 0,
  "errors": []
}
```

#### 状态码

- `200 OK`
- `400 Bad Request`
- `502 Bad Gateway`

---

### `DELETE /subscription`

将指定设备 token 从 `booking_open` topic 退订。

#### 请求体

```json
{
  "token": "fcm_device_token"
}
```

#### 响应示例

```json
{
  "topic": "booking_open",
  "success_count": 1,
  "failure_count": 0,
  "errors": []
}
```

#### 状态码

- `200 OK`
- `400 Bad Request`
- `502 Bad Gateway`

---

### `POST /sysbooking/login`

使用 `sb_refreshtoken` 刷新会话，校验返回的 `user.id` 与请求中的 `user_id` 是否一致。

如果一致：

- 生成一个 64 位随机本地 `token`
- 将会话信息写入本地 SQLite
- 同一个 `user_id` 可以同时保留多个有效 `token`

#### 请求体

```json
{
  "user_id": "052554e2-b3c9-40d9-a947-9c1da6f2a63d",
  "sb_refreshtoken": "xxx",
  "fcm_token": "optional_device_token"
}
```

#### 字段说明

- `user_id`：用户 ID
- `sb_refreshtoken`：刷新令牌
- `fcm_token`：可选，设备的 FCM token

#### 响应示例

```json
{
  "user_id": "052554e2-b3c9-40d9-a947-9c1da6f2a63d",
  "token": "64-char-local-token"
}
```

#### 状态码

- `200 OK`
- `400 Bad Request`
- `401 Unauthorized`
- `403 Forbidden`
- `500 Internal Server Error`

---

### `GET /sysbooking/tokenvalid`

查询当前会话对应的 Supabase `sb_token` 是否可用。

#### 说明

- 该接口仍然使用 `x-booking-token` 定位本地会话
- 如果会话存在且 `sb_token` 仍有效，返回 `valid: true`
- 如果会话存在但 `sb_token` 已失效，返回 `valid: false`
- 如果本地会话不存在，返回 `401 Unauthorized`

#### 请求头

- `x-booking-token`：本地登录 token，用于定位会话

#### 响应示例

```json
{
  "valid": true
}
```

#### 状态码

- `200 OK`
- `401 Unauthorized`
- `500 Internal Server Error`

---

### `GET /sysbooking/logout`

保留当前 `x-booking-token`，并将同一 `user_id` 下其他所有 token 置为失效。

#### 请求头

- `x-booking-token`：当前登录 token

#### 响应示例

```json
{
  "user_id": "052554e2-b3c9-40d9-a947-9c1da6f2a63d",
  "token": "64-char-local-token",
  "invalidated_count": 2,
  "current_token_valid": true
}
```

#### 状态码

- `200 OK`
- `401 Unauthorized`
- `500 Internal Server Error`

---

### `POST /sysbooking/booking`

创建一个 booking 记录。

#### 请求头

- `x-booking-token`：登录 token

#### 请求体

```json
{
  "maid_id": "maid_xxx",
  "timeslot": 21,
  "autoqueue": true,
  "with_friend": false,
  "friend_vrcid": ""
}
```

#### 字段说明

- `maid_id`：被预约的 maid ID
- `timeslot`：只能是 `21` 或 `22`
- `autoqueue`：是否标记为自动排队
- `with_friend`：是否带朋友一起预约
- `friend_vrcid`：朋友的 VRCID，可选，默认 `""`

#### 响应示例

```json
{
  "booking_id": "f4c3d2b1a09876543210fedcba987654"
}
```

#### 状态码

- `201 Created`
- `400 Bad Request`
- `401 Unauthorized`
- `403 Forbidden`
- `409 Conflict`
- `500 Internal Server Error`

---

### `DELETE /sysbooking/booking`

删除一个 booking 记录。

#### 请求头

- `x-booking-token`：登录 token

#### 请求体

```json
{
  "booking_id": "f4c3d2b1a09876543210fedcba987654"
}
```

#### 字段说明

- `booking_id`：要删除的 booking ID

#### 状态码

- `204 No Content`
- `400 Bad Request`
- `401 Unauthorized`
- `403 Forbidden`
- `404 Not Found`
- `409 Conflict`
- `500 Internal Server Error`

---

### `GET /sysbooking/queuelist`

查询当前登录用户所有状态为 `waiting` 的预约列表。

#### 请求头

- `x-booking-token`：登录 token

#### 响应示例

```json
[
  {
    "booking_id": "f4c3d2b1a09876543210fedcba987654",
    "maid_id": "maid_xxx",
    "with_friend": false,
    "friend_vrcid": "friend_vrcid_xxx",
    "timeslot": 21,
    "queue": 0,
    "autoqueue": true
  }
]
```

#### 字段说明

- `maid_id`：maid ID
- `booking_id`：预约 ID
- `with_friend`：是否带朋友一起预约
- `friend_vrcid`：朋友的 VRCID
- `timeslot`：预约时段
- `queue`：当前队列前面有多少人，`0` 表示前面没人
- `autoqueue`：是否标记为自动排队

#### 状态码

- `200 OK`
- `401 Unauthorized`
- `403 Forbidden`
- `500 Internal Server Error`

---

### `PUT /sysbooking/booking`

仅修改指定 booking 的 `autoqueue`、`with_friend` 和/或 `friend_vrcid`。

#### 请求头

- `x-booking-token`：登录 token

#### 请求体

```json
{
  "booking_id": "f4c3d2b1a09876543210fedcba987654",
  "autoqueue": true,
  "with_friend": false,
  "friend_vrcid": "friend_vrcid_xxx"
}
```

#### 字段说明

- `booking_id`：要修改的 booking ID
- `autoqueue`：新的自动排队状态，可选
- `with_friend`：新的是否带朋友状态，可选
- `friend_vrcid`：新的朋友 VRCID，可选

#### 说明

- 只会修改传入的字段
- 不会修改 `maid_id`、`timeslot`、`status` 等其他字段
- 仅允许修改当前登录用户自己的、且状态为 `waiting` 的 booking
- 至少需要传入 `autoqueue`、`with_friend` 或 `friend_vrcid` 其中一个

#### 响应示例

```json
{
  "booking_id": "f4c3d2b1a09876543210fedcba987654"
}
```

#### 状态码

- `200 OK`
- `400 Bad Request`
- `401 Unauthorized`
- `403 Forbidden`
- `404 Not Found`
- `409 Conflict`
- `500 Internal Server Error`

---

### `PUT /sysbooking/notification`

更新当前登录 token 对应会话的 FCM token 和通知开关。

#### 请求头

- `x-booking-token`：登录 token

#### 请求体

```json
{
  "fcm_token": "fcm_device_token",
  "notification": true
}
```

#### 字段说明

- `fcm_token`：新的设备 FCM token
- `notification`：是否开启通知
- 默认值为 `false`

#### 响应示例

```json
{
  "user_id": "052554e2-b3c9-40d9-a947-9c1da6f2a63d",
  "fcm_token": "fcm_device_token",
  "notification_enabled": true
}
```

#### 状态码

- `200 OK`
- `400 Bad Request`
- `401 Unauthorized`
- `500 Internal Server Error`
