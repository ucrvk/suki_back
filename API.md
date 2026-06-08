# API 文档

本文档只描述 Gin 暴露的 HTTP 接口。

## 通用约定

- 所有接口默认返回 JSON，图片接口除外。
- CORS 为宽松策略。
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

使用 `sb_refreshtoken` 刷新会话，校验返回的 `user.id` 与请求中的 `id` 是否一致。

如果一致：

- 生成一个 64 位随机本地 `token`
- 将会话信息写入本地 SQLite
- 同一个 `user_id` 已存在时直接覆盖更新

#### 请求体

```json
{
  "id": "052554e2-b3c9-40d9-a947-9c1da6f2a63d",
  "sb_refreshtoken": "xxx",
  "fcm_token": "optional_device_token"
}
```

#### 字段说明

- `id`：用户 ID
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

查询当前登录用户的 `sb_token` 是否可用。

#### 请求头

- `x-booking-token`：登录 token

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
  "with_friend": false
}
```

#### 字段说明

- `maid_id`：被预约的 maid ID
- `timeslot`：只能是 `21` 或 `22`
- `autoqueue`：是否标记为自动排队
- `with_friend`：是否带朋友一起预约

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

### `GET /sysbooking/queue`

查询当前登录用户在指定 `maid_id + timeslot` 队列中的排名。

#### 请求头

- `x-booking-token`：登录 token

#### 查询参数

- `maidid`：maid ID
- `timeslot`：只能是 `21` 或 `22`

#### 响应规则

- 如果用户在队列中，返回一个数字：
  - `0` 表示前面没人
  - `1` 表示前面有 1 个人
  - 以此类推
- 如果用户不在队列中，返回 `204 No Content`

#### 状态码

- `200 OK`
- `204 No Content`
- `400 Bad Request`
- `401 Unauthorized`
- `403 Forbidden`
- `500 Internal Server Error`
