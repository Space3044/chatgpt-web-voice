# chatgpt-web-voice 实现原理

本文档说明本项目的整体架构、核心数据流、关键模块职责与安全边界。面向希望理解「为什么这样设计、一次语音通话实际发生了什么」的开发者与运维者。

部署、环境变量与 API 字段说明见根目录 [README.md](../README.md)。

---

## 1. 项目定位

`chatgpt-web-voice` 是一个 **自托管 ChatGPT Web Voice 网关**。

它 **不** 使用 OpenAI 官方 Realtime API Key，而是：

1. 用管理员维护的 **ChatGPT Web `access_token` 账号池** 去访问 `chatgpt.com` 的 Web 语音入口；
2. 浏览器（或下游后端）只负责 **WebRTC 媒体与 DataChannel 事件**；
3. 本服务只负责 **鉴权、选号、SDP 信令代理、会话绑定、会话文本落库**。

关键边界：

| 谁负责 | 内容 |
|---|---|
| 浏览器 / 下游客户端 | 麦克风、扬声器、`RTCPeerConnection`、DataChannel、字幕渲染、业务 UI |
| Gateway | 登录 / API Key、账号池、向 `chatgpt.com/realtime/wm` 换 answer SDP、会话绑定、对话文本持久化 |
| chatgpt.com + Azure WebRTC | 语音推理、远端媒体轨、DataChannel 协议事件 |

Gateway **不接收、不存储原始通话音频**；只持久化 conversation 元数据与文本/字幕。

---

## 2. 总体架构

```text
┌─────────────────────────────────────────────────────────────────┐
│  Built-in UI (static/*.html)  或  Downstream backend            │
│  mic + RTCPeerConnection + DataChannel("oai-events")            │
└──────────────────────────────┬──────────────────────────────────┘
                               │  HTTP (session cookie / Basic / Bearer)
                               │  POST offer_sdp → answer_sdp
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│  Gateway (Go, stdlib net/http)                                  │
│                                                                 │
│  auth.Manager          浏览器会话 + Basic Auth                  │
│  auth.APIKeyManager    /v1 Bearer                               │
│  accounts.Pool         SQLite 账号池 + AES-GCM 密封 token       │
│  voice.Service         /realtime/wm 代理 + 内存 session 绑定    │
│  conversations.Store   文本会话 / 字幕                          │
│  apikeys.Store         下游 API Key（仅存 hash）                │
└──────────────────────────────┬──────────────────────────────────┘
                               │  multipart: sdp + session JSON
                               │  Authorization: Bearer <web token>
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│  chatgpt.com                                                    │
│  POST /realtime/wm?dcid=0                                       │
│  GET  /backend-api/settings/user   (账号探活)                   │
│                         │                                       │
│                         ▼                                       │
│              Azure WebRTC media plane                           │
│   (浏览器 ↔ 上游，不经 Gateway)                                  │
└─────────────────────────────────────────────────────────────────┘
```

设计取舍可以概括成一句话：

> **信令走 Gateway，媒体走浏览器直连上游。**

这样 Gateway 无需处理 RTP/音频，也不持有通话明文音频，实现简单、扩展路径清晰。

---

## 3. 启动与依赖装配

入口：`cmd/server` → `app.Run()`。

启动顺序（composition root）：

1. **配置**：`config.Load()` 读环境变量，`Validate()` 强制要求 `VOICE_AUTH_*` 与 `VOICE_TOKEN_ENCRYPTION_KEY`。
2. **日志**：`logging.New` 设为全局 `slog`。
3. **SQLite**：`store.Open` 打开 `VOICE_DATABASE_FILE`，执行 schema migrate（WAL、accounts / api_keys / conversations / messages）。
4. **密钥盒**：`secretbox.ParseKey` + `New` 构建 AES-256-GCM `Box`。
5. **账号池**：`accounts.NewPoolFromDB(db).WithBox(box)`，再 `SealStoredTokens()` 把遗留明文 token 就地密封。
6. **领域服务**：conversations、apikeys、voice.Service。
7. **鉴权**：浏览器 `auth.Manager`、下游 `auth.APIKeyManager`。
8. **HTTP 路由分层**（见下一节）。
9. **可选 TLS** 后 `ListenAndServe` / `ListenAndServeTLS`，SIGINT/SIGTERM 优雅退出。

共享一个 `store.DB` 连接与进程级 mutex，避免多 repository 各自打开 SQLite 导致锁竞争。

---

## 4. HTTP 路由与鉴权分层

根路由在 `app.newHandler` 中组装：

```text
root mux
├── GET  /login                    公开（已登录则跳 /voice）
├── POST /api/auth/login           公开
├── POST /api/auth/logout          Require(session/Basic)
├── GET  /api/auth/session         Require(session/Basic)
├── /v1/*                          APIKeyManager.Require → downstream mux
└── /*                             auth.Manager.Require → protected mux
                                     ├── /api/voice/*
                                     ├── /api/accounts/*
                                     ├── /api/keys/*
                                     ├── /api/conversations/*
                                     └── 静态页 /voice /accounts /keys
```

最外层还有：

- `logging.HTTPMiddleware`：request id、耗时、状态码；
- `securityHeaders`：`no-store`、`X-Frame-Options: DENY`、麦克风 Permissions-Policy 等。

### 4.1 管理员 / 浏览器鉴权

`auth.Manager` 支持两种方式：

| 方式 | 用途 |
|---|---|
| HttpOnly cookie `voice_gateway_session` | 浏览器登录；token 存 **进程内存**，重启需重新登录 |
| HTTP Basic | 脚本 / 运维探测 |

密码校验用 SHA-256 + `subtle.ConstantTimeCompare`，避免简单时序旁路。浏览器导航未登录时 303 到 `/login`；API 返回 JSON 401。

会话 owner 命名空间：

```text
admin:<username>
```

### 4.2 下游 API Key 鉴权

`/v1/*` **只能** 用 Bearer key（前缀 `vgw_live_`），不能访问管理页、账号池、对话历史。

- 创建时：随机 32 字节 → base64 → 完整 secret **只展示一次**；
- 存储：`secret_hash = SHA-256(secret)` + 显示用 `key_prefix`；
- 鉴权成功后 owner：

```text
api_key:<numeric_id>
```

`voice_session_id` 绑定到 owner，不同 API Key **不能** 复用或释放对方的绑定。

---

## 5. 一次语音通话的完整流程

以内置 `static/voice.html` 为例。

### 5.1 建立连接（信令）

```text
1. 浏览器 GET /api/voice/config
   ← voices / languages / STUN / DataChannel 约定

2. getUserMedia(麦克风)
3. new RTCPeerConnection({ iceServers, bundlePolicy: max-bundle })
4. createDataChannel("oai-events", { negotiated: true, id: 0 })
5. addTrack(本地音轨)
6. createOffer → setLocalDescription → 等待 ICE gathering（有超时）

7. POST /api/voice/session
   {
     offer_sdp,
     voice, voice_mode, language_code,
     voice_session_id?   // 重连时带上
   }

8. Gateway:
   a. 规范化 SDP（v=0 校验，换行统一 \r\n）
   b. 校验 voice / mode / language
   c. 按 owner + voice_session_id 取绑定；否则从账号池 Pick
   d. multipart POST https://chatgpt.com/realtime/wm?dcid=0
        fields: sdp, session(JSON)
        headers: Bearer <access_token>, oai-device-id, client version...
   e. 若 401 → MarkInvalid + 换号重试（最多 MaxAccountAttempts）
   f. 成功则 bindVoiceSession，返回 answer_sdp + voice_session_id

9. 浏览器 setRemoteDescription(answer)
10. ICE 连通后媒体直连上游；DataChannel 开始收发事件
```

### 5.2 通话中（媒体与事件，不经 Gateway）

| 方向 | 机制 | 作用 |
|---|---|---|
| 本地 → 远端 | WebRTC audio track | 用户语音 |
| 远端 → 本地 | WebRTC audio track | 助手语音 |
| 双向控制 | DataChannel `oai-events` | 状态、字幕、文本输入、打断 |

常见事件（客户端解析后更新 UI / 落库）：

- `state_update`：`listening` / `speaking` / `responding` / `idle`
- `chat_message_delta` 等：字幕增量
- 客户端发送 `relay_message`：通话中文本输入
- 客户端发送 `action_request: stop_speaking`：打断（含 RMS 自动 barge-in）

### 5.3 释放

- 内置页：`POST /api/voice/session/release` + `{ voice_session_id }`
- 下游：`DELETE /v1/voice/sessions/{id}`

释放只清除 Gateway 内存中的 **账号绑定**。已建立的 WebRTC 连接不会被服务端强制掐断；撤销 API Key 同样 **不能** 中断已连通的媒体面。

---

## 6. 核心模块原理

### 6.1 `voice.Service`：SDP 代理与会话绑定

文件：`internal/voice/service.go`

**职责**

1. 把浏览器 offer SDP 转成 ChatGPT Web 期望的 multipart 请求；
2. 构造 `session` JSON（voice、voice_mode、language_code、timezone 等）；
3. 维护 `map[voice_session_id]*sessionBinding`：

```text
sessionBinding {
  Owner, AccountID, AccessToken, DeviceID, Proxy,
  CreatedAt, UpdatedAt
}
```

**绑定语义**

| 请求情况 | 行为 |
|---|---|
| 无 `voice_session_id` | 新建 `vs_...`，Pick 账号 |
| 有 id 且属于当前 owner | 优先用绑定 token；失败可换号 |
| 有 id 但不属于当前 owner | 403 |
| 有 id 但已过期/不存在 | 404 |

TTL 由 `VOICE_SESSION_TTL_SECONDS` 控制；访问时刷新 `UpdatedAt`，定期清理过期项。

**为何需要绑定**

同一通话重连或二次 SDP 交换时，应尽量固定同一 ChatGPT 账号与 device_id/proxy，减少会话漂移与无谓换号。

**上游请求形态**

```text
POST https://chatgpt.com/realtime/wm?dcid=0
Content-Type: multipart/form-data
Authorization: Bearer <web access_token>
oai-device-id / oai-language / oai-client-version / oai-client-build-number
Origin/Referer/User-Agent 模拟浏览器

parts:
  sdp     = offer SDP
  session = JSON 配置
```

成功响应体是 **answer SDP 文本**（以 `v=0` 开头），不是 JSON。

**账号探活** `ProbeAccountToken`：

```text
GET /backend-api/settings/user
```

| 上游结果 | Gateway 行为 |
|---|---|
| 200 + JSON | `alive` |
| 401 | `unauthorized`，**禁用**该账号 |
| HTML 挑战 / 网络错误 / 其他 | `unknown`，**不**禁用（避免 Cloudflare 误杀） |

### 6.2 `accounts.Pool`：账号池与密封存储

文件：`internal/accounts/*`、`internal/secretbox/*`

**选号策略 `Pick`**

1. 若指定 preferred token：按 `token_hash` 精确命中且未禁用；
2. 否则：`disabled=0 AND status<>'禁用'`，按 **最久未用**（`last_used_at` NULL 优先，再升序）选；
3. 支持 `excluded` 集合，用于 401 后跳过已失败 token；
4. 选中后更新 `last_used_at`。

**密封（at-rest encryption）**

```text
plaintext access_token
    │
    ├─► secretbox.Seal → "enc1." + base64url(nonce||ciphertext)  写入 access_token 列
    └─► secretbox.Hash → HMAC-SHA256(key, plain) hex              写入 token_hash 列
```

要点：

- AES-256-GCM，随机 12 字节 nonce → 密文 **不可去重**，因此唯一性改由 `token_hash` 保证；
- 无 `enc1.` 前缀的旧明文在启动 `SealStoredTokens` 时就地迁移；
- 领域层读出后始终是明文；**API 列表永不返回完整 token**，只有 preview + JWT exp 展示字段。

**JWT 过期展示**（`tokenutil`）只解析 payload 的 `exp`，**不验签**，仅供面板预检。

### 6.3 `httpclient`：上游传输与代理

优先级：

1. 账号记录上的 `proxy`（http/https/socks5/socks5h）；
2. 进程环境 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`；
3. 直连。

Production 强制校验证书；development 才允许 `VOICE_SKIP_SSL_VERIFY`。

说明：Go 标准库 **不能** 完整复刻 Chrome TLS 指纹（curl_cffi 那类 impersonate）；本项目靠浏览器风格 HTTP 头 + 合法 web token 走 Web 路径。

### 6.4 `conversations.Store`：文本与字幕

- 按 `owner` 隔离；
- 消息以 `client_id` 幂等 upsert，便于流式字幕多次更新同一条；
- 只存文本，不存音频。

内置 voice 页在通话中把用户文本与助手 caption 写回 `/api/conversations/...`。

### 6.5 下游 `/v1` 集成面

目标：让第三方后端 **自建 WebRTC 客户端**，只把「换 SDP」委托给 Gateway。

| 端点 | 作用 |
|---|---|
| `GET /v1/health` | 鉴权健康检查 |
| `GET /v1/voice/config` | 非机密能力文档（音色、语言、STUN、DataChannel 约定） |
| `POST /v1/voice/sessions` | offer → answer |
| `DELETE /v1/voice/sessions/{id}` | 释放绑定 |

响应 **绝不** 包含 account id、email、token、proxy、池状态。下游必须自己实现：

- `RTCPeerConnection` + 约定 DataChannel；
- 字幕 / 业务会话 / 持久化；
- 密钥保管（Key 放后端，不要塞进浏览器）。

`voice.Config()` 是这份能力文档的唯一来源，内置页与 `/v1` 共用。

---

## 7. 前端职责（内置页）

| 页面 | 作用 |
|---|---|
| `/login` | 账号密码登录，拿 session cookie |
| `/voice` | 主工作台：通话、字幕、会话历史、设置抽屉 |
| `/accounts` | 账号池 CRUD、探活、JWT 过期展示 |
| `/keys` | 下游 API Key 创建（一次性 secret）/ 启停 / 删除 |

`voice.html` 关键路径：

1. 拉 `/api/voice/config` 填音色、语言、ICE；
2. 建立 PC + negotiated DataChannel；
3. SDP 交换；
4. 解析 DataChannel 事件更新状态机与字幕；
5. 可选 RMS 检测自动 `stop_speaking`；
6. 文本通过 `relay_message` 提交；
7. 会话列表走 conversations API。

媒体路径在浏览器与上游之间，Gateway 只在步骤 3 短暂参与。

---

## 8. 数据模型（SQLite）

默认路径：`data/voice.db`（WAL）。

### accounts

| 字段 | 含义 |
|---|---|
| `access_token` | 密封密文（`enc1....`） |
| `token_hash` | HMAC 摘要，唯一索引 |
| `device_id` | 上游 `oai-device-id` |
| `proxy` | 可选账号级代理 |
| `disabled` / `status` | 可用性；401 后置禁用 |
| `last_used_at` | LRU 选号 |

### api_keys

| 字段 | 含义 |
|---|---|
| `secret_hash` | SHA-256，唯一 |
| `key_prefix` | UI 展示 |
| `enabled` | 启停 |

### conversations / conversation_messages

按 owner 隔离的文本会话；messages 用 `(conversation_id, client_id)` 唯一约束支持字幕 upsert。

运行时 **内存态**（不落库）：

- 浏览器 session cookie → token 映射；
- `voice_session_id` → 账号绑定。

---

## 9. 安全模型

```text
┌──────────────┐     cookie / Basic      ┌──────────────┐
│  管理员浏览器 │ ─────────────────────► │  管理面 APIs │
└──────────────┘                         │  静态管理页  │
                                         └──────┬───────┘
                                                │ 仅服务端内存/DB
                                                ▼
                                         sealed access_token
                                                │
┌──────────────┐     Bearer API Key      ┌──────┴───────┐
│  下游后端    │ ─────────────────────► │   /v1 only   │
└──────────────┘                         └──────────────┘
       │
       │ WebRTC media（直连 chatgpt.com）
       ▼
   上游媒体面
```

要点：

1. **浏览器永不持有** ChatGPT `access_token`；
2. Token 落盘必须经 `VOICE_TOKEN_ENCRYPTION_KEY` 密封；丢 key ≈ 丢账号池；
3. 下游 Key 只存 hash，创建时一次性展示；
4. owner 命名空间隔离 admin 与各 API Key 的 voice session；
5. 列表 API 脱敏：token preview、proxy 无密码 preview；
6. 生产建议：网关内网 HTTP + 反向代理 HTTPS，勿把管理面裸奔公网。

---

## 10. 包结构与依赖方向

```text
cmd/server, cmd/migrate-accounts
        │
        ▼
internal/app          装配、TLS、静态路由、root mux
        │
        ├── internal/api          HTTP handlers（只依赖 domain interface）
        ├── internal/auth         会话 / Basic / API Key middleware
        ├── internal/voice        上游 SDP + probe
        ├── internal/accounts     账号 repository
        ├── internal/apikeys      API Key repository
        ├── internal/conversations
        ├── internal/store        SQLite open + migrate
        ├── internal/secretbox    AES-GCM seal/open + HMAC hash
        ├── internal/httpclient   上游 transport
        ├── internal/config
        ├── internal/logging
        ├── internal/tokenutil
        └── internal/tlsutil
```

`api` 层通过 interface（`VoiceService`、`AccountStore`…）依赖领域，避免 handler 直接绑死具体存储实现，便于测试替换。

---

## 11. 与官方 Realtime API 的差异

| 维度 | 本项目（Web Voice 路径） | OpenAI Realtime API |
|---|---|---|
| 凭证 | ChatGPT **Web** `access_token` 池 | 官方 API Key |
| 信令入口 | `chatgpt.com/realtime/wm` | `api.openai.com` realtime 端点 |
| 客户端 | 浏览器 WebRTC + 约定 DataChannel | 官方协议 / SDK |
| 媒体 | 浏览器 ↔ Azure WebRTC | 依官方实现 |
| 适用场景 | 自托管网关、复用 web 订阅能力 | 正规 API 产品集成 |

本项目是 **非官方 web 路径网关**，需遵守 OpenAI ToS 与当地法律；token 过期、风控、HTML 挑战属于上游行为，Gateway 只做探测与换号。

---

## 12. 失败与重试策略（摘要）

| 场景 | 策略 |
|---|---|
| offer SDP 非法 | 400，不访问上游 |
| voice / language 非法 | 400 |
| 无可用账号 | 503 |
| 上游网络错误 | 502 |
| 上游 401 | 禁用该账号，换号重试，耗尽则 401/503 |
| 上游非 2xx 或非 SDP 响应 | 502（不盲目换号，避免把业务错误当 token 问题） |
| probe HTML/超时 | `unknown`，不禁用 |
| 绑定 session 属他人 | 403 |
| 绑定 session 丢失 | 404 |

---

## 13. 阅读代码的推荐顺序

1. `cmd/server/main.go` → `internal/app/app.go`（装配与路由）
2. `internal/voice/service.go`（CreateSession / postWMOnce / 绑定）
3. `internal/accounts/pool.go` + `internal/secretbox/box.go`（选号与密封）
4. `internal/api/voice.go` + `downstream.go`（管理面 vs 下游面）
5. `internal/auth/*`（两套鉴权）
6. `static/voice.html` 中 `startCall` / DataChannel 处理（客户端闭环）
7. `internal/store/schema.go`（持久化形状）

---

## 14. 一句话总结

**chatgpt-web-voice 把 ChatGPT 网页版语音通话拆成「可池化的信令代理」与「浏览器直连的媒体面」：用密封存储的 web token 账号池向 `/realtime/wm` 换 SDP，用内存绑定固定会话账号，用 SQLite 管理账号、下游 Key 与文本历史，同时把原始音频与上游凭证挡在浏览器与磁盘明文之外。**
