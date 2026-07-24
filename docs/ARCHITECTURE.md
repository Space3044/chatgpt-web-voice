# chatgpt-web-voice 实现原理

本文档说明本项目的整体架构、核心数据流、关键模块职责与安全边界。面向希望理解「为什么这样设计、一次语音通话实际发生了什么」的开发者与运维者。

部署、环境变量与下游接入示例见根目录 [README.md](../README.md)。变更摘要见 [CHANGELOG.md](../CHANGELOG.md)。

---

## 1. 项目定位

`chatgpt-web-voice` 是一个 **自托管 ChatGPT Web Voice 网关**。

它 **不** 使用 OpenAI 官方 Realtime API Key，而是：

1. 用管理员维护的 **ChatGPT Web `access_token` 账号池** 访问 `chatgpt.com` 的 Web 语音入口；
2. 浏览器（或下游后端）负责 **WebRTC 媒体与 DataChannel 事件**；
3. 本服务负责 **鉴权、选号、SDP 信令代理、会话绑定、粘性续聊元数据、可选图片上传凭证、会话文本落库**。

| 谁负责 | 内容 |
|---|---|
| 浏览器 / 下游客户端 | 麦克风、扬声器、`RTCPeerConnection`、DataChannel、字幕、业务 UI；图片字节直传 Azure |
| Gateway | 登录 / API Key、账号池、`/realtime/wm` SDP 代理、内存绑定 + `call_sessions`、文本会话、图片凭证与 complete |
| chatgpt.com + Azure | 语音推理、WebRTC 媒体面、文件 blob、DataChannel 协议事件 |

**设计边界：信令 / 凭证走 Gateway，媒体与图片字节尽量不经 Gateway。**

- Gateway **不接收、不存储原始通话音频**；
- 图片上传：**不收图、不落库** `file_id` 与字节，只代持 token 申请 SAS / complete；
- 账号仅使用 `access_token`，**不会自动 refresh**；过期需在管理端更换。

---

## 2. 总体架构

```text
┌──────────────────────────────────────────────────────────────────┐
│  Built-in UI (static/*.html)  或  Downstream backend               │
│  mic + RTCPeerConnection + DataChannel("oai-events")             │
│  可选：图片 PUT → Azure SAS                                       │
└───────────────────────────────┬──────────────────────────────────┘
                                │  HTTP
                                │  cookie session / Bearer API Key
                                │  offer_sdp → answer_sdp
                                │  uploads credential / complete
                                ▼
┌──────────────────────────────────────────────────────────────────┐
│  Gateway (Go, stdlib net/http)                                   │
│                                                                  │
│  auth.Manager           浏览器 HttpOnly 会话（可落 SQLite）        │
│  auth.APIKeyManager     /v1 Bearer（仅 hash）                     │
│  accounts.Pool          账号池 + AES-GCM 密封 token               │
│  voice.Service          /realtime/wm + 内存绑定 + 标题/探活      │
│  voice.upload           files 凭证 + complete（无图落库）          │
│  callsessions.Store     会话元数据（无聊天正文）                   │
│  conversations.Store    本地文本会话 / 字幕 / title_locked         │
│  apikeys.Store          下游 API Key                             │
└───────────────────────────────┬──────────────────────────────────┘
                                │  multipart: sdp + session JSON
                                │  Authorization: Bearer <web token>
                                │  POST /backend-api/files …
                                ▼
┌──────────────────────────────────────────────────────────────────┐
│  chatgpt.com                                                     │
│  POST /realtime/wm?dcid=0                                        │
│  GET  /backend-api/settings/user     (探活)                      │
│  GET  /backend-api/conversation/{id} (标题)                      │
│  POST /backend-api/files …           (图片凭证 / complete)       │
│                         │                                        │
│                         ▼                                        │
│              Azure WebRTC media + Azure Blob (SAS)               │
│   媒体：浏览器 ↔ 上游；图片字节：下游 ↔ Blob（均不经 Gateway）      │
└──────────────────────────────────────────────────────────────────┘
```

一句话：

> **信令与 token 绑定走 Gateway；媒体与图片字节直连上游。**

---

## 3. 启动与依赖装配

入口：`cmd/server` → `app.Run()`。

启动顺序（composition root）：

1. **配置**：`config.Load()` / `Validate()`，强制 `VOICE_AUTH_*` 与 `VOICE_TOKEN_ENCRYPTION_KEY`。
2. **日志**：`logging.New` → 全局 `slog`。
3. **SQLite**：`store.Open` + schema migrate（WAL、accounts / api_keys / conversations / messages / call_sessions / auth_sessions）。
4. **密钥盒**：`secretbox` AES-256-GCM。
5. **账号池**：`accounts.NewPoolFromDB` + `WithBox` + `SealStoredTokens`。
6. **领域**：conversations、apikeys、callsessions、`voice.Service.WithCallSessions`。
7. **鉴权**：浏览器 `auth.Manager`（可选 durable session store）、下游 `auth.APIKeyManager`。
8. **路由分层**（见第 4 节）。
9. **可选 TLS** 后监听；SIGINT/SIGTERM 优雅退出。
10. 启动时可将残留 **active** `call_sessions` 标为 released（进程重启后内存绑定已丢）。

共享一个 `store.DB` 与进程级 mutex，避免多 repository 各自开库抢锁。

---

## 4. HTTP 路由与鉴权分层

根路由在 `app.newHandler` 中组装：

```text
root mux
├── GET  /login                         公开（已登录 → /voice）
├── POST /api/auth/login                公开
├── POST /api/auth/logout               Require(session)
├── GET  /api/auth/session              Require(session)
├── GET  /static/*                      公开静态资源（登录页 CSS 等）
├── /v1/*                               APIKeyManager.Require → downstream
└── /*                                  auth.Manager.Require → protected
      ├── /api/voice/*
      ├── /api/accounts/*
      ├── /api/keys/*
      ├── /api/call-sessions/*
      ├── /api/conversations/*
      └── 页面 /voice /accounts /keys /sessions
```

最外层：

- `logging.HTTPMiddleware`：request id、耗时、状态码；
- `securityHeaders`：`Cache-Control: no-store`、`X-Frame-Options: DENY`、麦克风 Permissions-Policy 等。

### 4.1 管理员 / 浏览器鉴权

| 方式 | 用途 |
|---|---|
| HttpOnly cookie `voice_gateway_session` | 浏览器登录 |
| CSRF cookie `voice_gateway_csrf` + 头 `X-CSRF-Token` | 对 cookie 会话的非安全方法校验 |

**管理面不支持 HTTP Basic**（脚本请用登录 cookie 或下游 `/v1` API Key）。

- 密码：SHA-256 + `subtle.ConstantTimeCompare`；
- 登录失败：窗口计数 + 锁定（`VOICE_LOGIN_*`）；
- 会话可落 **auth_sessions**（token hash），进程重启后仍可续 cookie（在 TTL 内）；
- 「记住登录」控制 cookie 是否持久；未勾选则为浏览器会话 cookie。

Owner 命名空间：

```text
admin:<username>
```

### 4.2 下游 API Key 鉴权

`/v1/*` **只能** Bearer（前缀 `vgw_live_`），不能访问管理页、账号池、对话历史。

- 创建：随机 secret **只展示一次**；
- 存储：`secret_hash = SHA-256(secret)` + `key_prefix`；
- Owner：

```text
api_key:<numeric_id>
```

`voice_session_id` 按 owner 隔离；跨 Key 访问 → 403。

---

## 5. 一次语音通话的完整流程

以内置 `static/voice.html` 为主；下游 `/v1` 信令语义相同，只是鉴权与响应字段更克制。

### 5.1 建立连接（信令）

```text
1. GET /api/voice/config  （或 /v1/voice/config）
   ← voices / languages / STUN / DataChannel 约定

2. getUserMedia → RTCPeerConnection({ iceServers, bundlePolicy: max-bundle })
3. createDataChannel("oai-events", { negotiated: true, id: 0 })
4. addTrack(本地音轨) → createOffer → setLocalDescription → ICE gather

5. POST /api/voice/session  或  POST /v1/voice/sessions
   {
     offer_sdp,
     voice, voice_mode, language_code,
     voice_session_id?          // 重连
     // 管理面还可带 account_id / 上游续聊字段；下游不接受池内账号字段
   }

6. Gateway:
   a. 规范化 SDP（v=0，换行 \r\n）
   b. 校验 voice / mode / language
   c. 绑定：内存 → 否则 call_sessions sticky → 否则 Pick / PickByID
   d. multipart POST https://chatgpt.com/realtime/wm?dcid=0
   e. 401 → MarkInvalid + 换号（最多 MaxAccountAttempts）
   f. 成功：内存 bind + call_sessions Upsert → 返回 answer_sdp + voice_session_id
      （管理面响应可含 account_id / 上游 id；下游仅公开信令字段）

7. setRemoteDescription(answer) → ICE 连通 → DataChannel 收发
```

### 5.2 通话中（媒体与事件，不经 Gateway）

| 方向 | 机制 | 作用 |
|---|---|---|
| 本地 → 远端 | WebRTC audio | 用户语音 |
| 远端 → 本地 | WebRTC audio | 助手语音 |
| 双向控制 | DataChannel `oai-events` | 状态、字幕、文本、打断、图片指针 |

常见事件：

- `state_update`：`listening` / `speaking` / `responding` / `idle` …
- `startup_telemetry` / `conversation_update`：学习 `conversation_id` 等
- `chat_message_delta`：字幕
- 客户端 `relay_message`：文本或 `image_asset_pointer`
- 客户端 `action_request: stop_speaking`：打断（含 RMS barge-in）

学到上游 id 后：

- 内置页：`POST /api/voice/session/context` + `PATCH` 本地 conversation；
- 下游：`POST /v1/voice/sessions/{id}/context`。

### 5.3 挂断与释放

```text
内置页 stopCall:
  1. 关掉 PC / mic / DC
  2. persist 上游 id → 本地 conversation + gateway context
  3. 若 title_locked=false → GET 上游标题并写回本地 title
  4. POST /api/voice/session/release   // 清内存绑定；call_sessions → released

下游:
  DELETE /v1/voice/sessions/{id}
```

- 释放 **不** 强制掐断已建立的 WebRTC（客户端自己 close）；
- 撤销 API Key **不能** 中断已连通媒体面；
- 粘性账号与上游线索仍在 SQLite，供下次同 `voice_session_id` 重连。

---

## 6. 核心模块

### 6.1 `voice.Service`：SDP、绑定、探活、标题

文件：`internal/voice/service.go`、`config.go`、`upload.go`

**内存绑定** `map[voice_session_id]*sessionBinding`：

```text
Owner, AccountID, AccessToken, Proxy,
UpstreamVoiceSessionID, ConversationID, ParentMessageID,
CreatedAt, UpdatedAt
```

| 请求情况 | 行为 |
|---|---|
| 无 `voice_session_id` | 新建 `vs_...`，选号 |
| 有 id 且属当前 owner | 优先绑定 token / sticky account |
| 有 id 属他人 | 403 |
| 有 id 但内存无、SQLite 有 | 从 `call_sessions` 恢复 sticky 再拨 |
| 有 id 完全未知 | 404 |

TTL：`VOICE_SESSION_TTL_SECONDS`；访问刷新 `UpdatedAt`；超时 reap 并 `MarkReleased`。

**上游 SDP 请求**

```text
POST https://chatgpt.com/realtime/wm?dcid=0
Content-Type: multipart/form-data
Authorization: Bearer <web access_token>
oai-device-id / oai-session-id / oai-language / oai-client-version / oai-client-build-number
Sec-Ch-Ua* / Sec-Fetch-* / Origin / Referer / User-Agent（浏览器人设）

parts:
  sdp     = offer SDP
  session = JSON（见 6.1.1）
```

成功体为 **answer SDP 文本**（`v=0` 开头），非 JSON。

**探活** `ProbeAccountToken`：

```text
GET /backend-api/settings/user
```

| 结果 | 行为 |
|---|---|
| 200 + JSON | alive |
| 401 | unauthorized，**禁用**账号 |
| HTML 挑战 / 网络错误 / 其他 | unknown，**不**禁用 |

**标题** `FetchUpstreamTitle`：

```text
GET /backend-api/conversation/{upstream_conversation_id}
→ 只返回 title / has_title（无 token、无全文 mapping）
```

可用内存绑定 token；绑定已释放时从 `call_sessions` + `PickByID` 再取 token。

#### 6.1.1 session JSON 中的模型相关字段

`buildSessionJSON` 当前写入（与网页默认对齐）：

| 字段 | 当前值 | 说明 |
|---|---|---|
| `voice` / `voice_mode` / `language_code` | 请求规范化后的值 | 音色与模式 |
| `voice_session_id` 等 | 上游 UUID | 续聊线索 |
| `conversation_id` / `parent_message_id` | 可选 | best-effort 续线程 |
| `requested_default_model` | `""` | 未指定，服务端默认 |
| `model_slug` | `""` | 未指定主模型 |
| `model_slug_advanced` | `""` | 未指定 advanced 模型 |
| `backend_reasoning_effort` | `"instant"` | 语音路径默认推理强度 |

网关 **未对外暴露** model 配置项。空字符串是合法默认；可填值需以账号 `GET /backend-api/models` 的 `slug` 及官方语音 HAR 为准，语音路径是否采纳非空 model **未在本仓库做实锤验证**。

### 6.2 图片上传凭证（直传）

文件：`internal/voice/upload.go`

目标：下游只拿 **短时 SAS**，图片字节不经 Gateway，token 不离开 Gateway。

```text
1) POST .../uploads
     body: file_name, file_size, mime_type, width?, height?
     必须：活跃内存 voice_session 绑定（挂断后仅 SQLite 不够）

2) Gateway 用该 session 粘性账号：
     POST https://chatgpt.com/backend-api/files
     → file_id + upload_url

3) 下游 PUT upload_url（Azure SAS，直传字节）

4) POST .../uploads/{file_id}/complete
     Gateway: POST .../files/{file_id}/uploaded
     （失败可 fallback process_upload_stream）

5) 下游 DataChannel relay_message：
     asset_pointer: sediment://{file_id}
```

约束：

- owner + live binding；跨会话 403/404；
- mime 白名单（jpeg/png/webp/gif）、体积上限；
- 响应无 `access_token` / `account_id` / proxy；
- **不落库** 图片与 `file_id`（仅日志）。

管理面镜像：`/api/voice/session/uploads` 与 `.../complete`（需 CSRF）。

浏览器直连 Azure 可能受 CORS 限制；下游服务端代 PUT 通常更稳。

### 6.3 `accounts.Pool`：选号与密封

**Pick**

1. preferred token（`token_hash`）且未禁用；
2. 否则最久未用（`last_used_at`）；
3. `excluded` 跳过已 401 的 token；
4. `PickByID` 用于 sticky resume，账号缺失/禁用 **fail closed**。

**密封**

```text
plaintext access_token
  ├─ Seal → "enc1." + base64url(nonce||ciphertext)  → access_token 列
  └─ Hash → HMAC-SHA256 hex                         → token_hash 列
```

- 密文不可去重 → 唯一性靠 `token_hash`；
- 启动 `SealStoredTokens` 迁移遗留明文；
- 列表 API 永不返回完整 token。

### 6.4 `httpclient`：上游传输与代理

代理优先级：账号 `proxy` → 进程 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` → 直连。

| `VOICE_UPSTREAM_TRANSPORT` | 说明 |
|---|---|
| `curl-impersonate` | Docker 默认；镜像内 `curl_edge101` |
| `tls-client` | 本地 `go run` 默认；profile 如 `chrome_120` |
| `go` | stdlib TLS 回退 |

Production 强制校验证书；development 才允许 `VOICE_SKIP_SSL_VERIFY`。

浏览器人设默认对齐 ChatGPT2API-GO（Edge UA / Client Hints / OAI client 头）。  
`VOICE_DEVICE_ID` / `VOICE_SESSION_ID` 进程全局；未设则启动生成 UUID。

### 6.5 `callsessions.Store`

**无聊天正文** 的网关会话元数据：谁建连、sticky `account_id`、上游 id、voice 选项、active/released。

用途：

- 管理页 `/sessions`；
- 进程重启后下游/管理面凭 `voice_session_id` 粘回账号与续聊线索。

### 6.6 `conversations.Store`

按 `owner` 隔离的本地工作区（内置页为主）：

| 能力 | 说明 |
|---|---|
| 消息 | `(conversation_id, client_id)` 幂等 upsert，服务字幕流式更新 |
| 上游字段 | `account_id`、upstream / gateway session ids |
| `title_locked` | 用户手动改名后为 1；挂断不再拉上游标题覆盖 |
| 首条消息 | 标题为空且未 lock 时，可用首条 user 内容填 title |

只存文本，不存音频/图片字节。

### 6.7 下游 `/v1`

| 端点 | 作用 |
|---|---|
| `GET /v1/health` | 鉴权健康检查 |
| `GET /v1/voice/config` | 音色 / 语言 / STUN / DataChannel |
| `POST /v1/voice/sessions` | offer → answer；返回 `voice_session_id` 等公开字段 |
| `POST .../context` | 上报上游 id |
| `GET .../title` | 拉上游标题 |
| `POST .../uploads` | 图片直传凭证 |
| `POST .../uploads/{file_id}/complete` | 上游 complete |
| `DELETE .../{id}` | 释放内存绑定 |

下游响应 **不包含** pool `account_id`、token、proxy、上游账号信息。  
`voice.Config()` 是能力文档唯一来源，与内置页共用。

---

## 7. 前端职责（内置页）

| 页面 | 作用 |
|---|---|
| `/login` | 登录拿 session cookie |
| `/voice` | 通话、字幕、会话历史、设置、标题策略 |
| `/accounts` | 账号池 CRUD、探活、JWT exp 展示 |
| `/keys` | 下游 Key（一次性 secret） |
| `/sessions` | `call_sessions` 元数据 |

### 7.1 `voice.html` 主路径

1. `/api/voice/config` 填音色、语言、ICE；
2. PC + negotiated DataChannel；
3. SDP 交换；
4. 解析 DC 事件；可选 RMS → `stop_speaking`；
5. 文本 `relay_message`；
6. 会话 CRUD / 消息落库；
7. 挂断：persist context → **拉标题（若未 lock）** → release。

### 7.2 本地标题策略

| 阶段 | 行为 |
|---|---|
| 新会话 | 标题为空 /「新会话」 |
| 通话中首条用户话（语音字幕或打字） | 临时标题（STT 增量可伸长） |
| 通话中 | **不**轮询上游标题；忽略 mid-call `title_generation` 覆盖 |
| **每次**挂断 | `title_locked=false` 时请求一次上游标题并写回 |
| 用户重命名对话框 | `PATCH` 带 `title_locked=true` 持久化；之后挂断 skip |
| 上游 `has_title=false` | 保留当前本地标题 |

重连同一本地会话再挂断仍会请求（除非 locked）。  
「已应用过某 conversation_id」**不**阻止下一次挂断拉取。

---

## 8. 数据模型（SQLite）

默认：`data/voice.db`（WAL）。

### accounts

| 字段 | 含义 |
|---|---|
| `access_token` | 密封密文 `enc1....` |
| `token_hash` | HMAC，唯一 |
| `proxy` | 可选账号代理 |
| `disabled` / `status` | 可用性 |
| `last_used_at` | LRU |

指纹不在账号行：进程全局 `VOICE_DEVICE_ID` / `VOICE_SESSION_ID`。

### api_keys

`secret_hash`、`key_prefix`、`enabled`、`name` …

### conversations / conversation_messages

| 字段 | 含义 |
|---|---|
| `title` / `preview` | 展示 |
| `title_locked` | 用户改名锁定 |
| `account_id` | sticky 池账号 |
| `upstream_*` / `gateway_voice_session_id` | 续聊 |
| messages | 文本；`(conversation_id, client_id)` 唯一 |

### call_sessions

网关语音会话元数据（无聊天正文）：owner、caller、account_id、上游 id、voice 选项、active/released、时间戳。

### auth_sessions

浏览器登录 token hash + 过期时间，支撑进程重启后 cookie 续期。

### 运行时内存

- 浏览器 session token → 用户（可与 SQLite 双写）；
- `voice_session_id` → `sessionBinding`（含明文 token，仅进程内）。

---

## 9. 安全模型

```text
管理员浏览器 ── cookie + CSRF ──► 管理面 / 静态管理页
                                      │
                                      ▼
                               sealed access_token（SQLite）
                                      │
下游后端 ──── Bearer API Key ────────► /v1 only
     │
     ├── WebRTC 媒体 ───────────────► chatgpt.com / Azure（直连）
     └── 图片 PUT ──────────────────► Azure SAS（直连）
```

1. 浏览器 **永不持有** ChatGPT `access_token`；
2. 落盘 token 必须 `VOICE_TOKEN_ENCRYPTION_KEY`；丢 key ≈ 丢池；
3. 下游 Key 只存 hash；
4. owner 隔离 admin 与各 API Key 的 voice session / 对话；
5. 列表 API 脱敏；
6. 生产：网关内网 HTTP + 反代 HTTPS，管理面勿裸奔；
7. 图片：token 与 complete 留在 Gateway，字节不落盘。

---

## 10. 包结构与依赖方向

```text
cmd/server, cmd/migrate-accounts
        │
        ▼
internal/app              装配、TLS、静态路由、root mux
        │
        ├── internal/api           HTTP handlers（domain interface）
        ├── internal/auth          会话 / CSRF / API Key
        ├── internal/voice         SDP、绑定、探活、标题、上传凭证
        ├── internal/accounts
        ├── internal/apikeys
        ├── internal/callsessions
        ├── internal/conversations
        ├── internal/store         SQLite open + migrate
        ├── internal/secretbox
        ├── internal/httpclient
        ├── internal/config
        ├── internal/logging
        ├── internal/tokenutil
        └── internal/tlsutil
```

`api` 通过 interface 依赖领域，便于测试替换。

---

## 11. 与官方 Realtime API 的差异

| 维度 | 本项目（Web Voice） | OpenAI Realtime API |
|---|---|---|
| 凭证 | ChatGPT **Web** `access_token` 池 | 官方 API Key |
| 信令 | `chatgpt.com/realtime/wm` | `api.openai.com` realtime |
| 客户端 | 浏览器 WebRTC + 约定 DC | 官方协议 / SDK |
| 媒体 | 浏览器 ↔ Azure WebRTC | 依官方 |
| 模型字段 | session 内多为空 + `instant` | 官方 model 参数 |

本项目是 **非官方 web 路径网关**。token 过期、风控、Cloudflare HTML 挑战属上游行为；Gateway 做探测、换号与分类，不假装官方产品。

---

## 12. 失败与重试策略（摘要）

| 场景 | 策略 |
|---|---|
| offer SDP 非法 | 400 |
| voice / language 非法 | 400 |
| 无可用账号 | 503 |
| 上游网络错误 | 502 |
| 上游 401 | 禁用账号，换号重试 |
| 上游非 2xx / 非 SDP | 502（不盲目换号） |
| probe HTML/超时 | `unknown`，不禁用 |
| 绑定属他人 | 403 |
| 绑定丢失 | 404 |
| sticky 账号不可用 | fail closed（不静默换号 resume） |
| 图片无 live binding | 404 |
| 图片 mime/体积非法 | 400 |
| 标题未生成 | `has_title=false`，保留本地标题 |

---

## 13. 上游会话续聊（best-effort）

| 字段 | 存哪里 | 作用 |
|---|---|---|
| Gateway `voice_session_id` (`vs_...`) | 内存 + call_sessions + 本地 conversation | 会话句柄 |
| Pool `account_id` | 同上 | sticky 账号 |
| Upstream `voice_session_id` (UUID) | 绑定 + SQLite | 写入 wm session JSON |
| `conversation_id` / `parent_message_id` | DC 学习 → 绑定 + SQLite | 尝试续线程 |
| caller | call_sessions | 管理页可见 |

流程：

1. 首次建连：选号 + 上游 UUID + SDP；写 call_sessions active。
2. DC 学习 id → context API + 本地 PATCH。
3. 挂断：persist → 可选拉标题 → release 内存；call_sessions released，元数据保留。
4. 同 `voice_session_id` 再拨：内存或 SQLite sticky → 同账号 + 尽量同 conversation。
5. 新建本地 chat：release 并清空本地上游字段与 sticky account。

本地 SQLite 历史可读 ≠ 模型上下文一定连续；上游可拒绝 resume。

---

## 14. 阅读代码的推荐顺序

1. `cmd/server/main.go` → `internal/app/app.go`
2. `internal/voice/service.go`（CreateSession / 绑定 / 标题）
3. `internal/voice/upload.go`（图片凭证）
4. `internal/accounts` + `internal/secretbox`
5. `internal/callsessions` + `internal/conversations`（含 `title_locked`）
6. `internal/api/voice.go` + `downstream.go`
7. `internal/auth/*`
8. `static/voice.html`：`startCall` / DC / `stopCall` / 标题
9. `internal/store/schema.go`

---

## 15. 一句话总结

**chatgpt-web-voice 把 ChatGPT 网页语音拆成「可池化的信令与凭证代理」和「客户端直连的媒体 / 图片字节面」：用密封 web token 账号池向 `/realtime/wm` 换 SDP，用内存绑定 + `call_sessions` 粘账号并 best-effort 续对话，用直传凭证支持通话中图片，用 SQLite 管理账号、Key、会话元数据与本地文本（含可锁定的标题），同时把原始音频、图片字节与上游明文 token 挡在浏览器与磁盘明文之外。**
