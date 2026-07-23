# chatgpt-web-voice

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev/)
[![GHCR](https://img.shields.io/badge/ghcr.io-space3044%2Fchatgpt--web--voice-2496ED)](https://github.com/Space3044/chatgpt-web-voice/pkgs/container/chatgpt-web-voice)

自托管的 **ChatGPT.com Web Voice 网关**。

浏览器或下游后端负责 WebRTC 媒体与 DataChannel；本服务负责账号池、向 `chatgpt.com/realtime/wm` 做 SDP 信令代理、语音会话绑定，以及必要的元数据持久化。使用自有 ChatGPT Web `access_token` 池，**不需要** OpenAI 官方 Realtime API Key。上游 token 在 SQLite 中以 AES-256-GCM 密封存储，不会完整返回给浏览器或下游。

**设计边界：信令走网关，媒体由客户端直连上游。** 网关不接收、不存储原始通话音频。

## 功能概览

- 实时语音（`/realtime/wm` + WebRTC + DataChannel）
- 通话中文本与字幕（DataChannel）
- 自动打断（barge-in）
- 管理端：账号池、下游 API Key、会话元数据、内置语音页
- 下游 `/v1` 接入：只持 API Key + `voice_session_id` 即可建连 / 恢复
- 粘性账号与上游续聊线索由网关持久化，不向下游暴露池内账号信息

## 本地启动

需要 Go 1.22+。

```bash
cp .env.example .env
# 必填：
#   VOICE_AUTH_USERNAME
#   VOICE_AUTH_PASSWORD
#   VOICE_TOKEN_ENCRYPTION_KEY   # openssl rand -hex 32
```

```bash
set -a && source .env && set +a
go run ./cmd/server
# 打开 http://127.0.0.1:8090/voice
```

或使用辅助脚本（自动加载 `.env`）：

```bash
bash ./scripts/dev.sh
```

登录后常用页面：

| 路径 | 说明 |
|---|---|
| `/voice` | 内置语音工作台 |
| `/accounts` | ChatGPT Web 账号池 |
| `/keys` | 下游 API Key（完整密钥只显示一次） |
| `/sessions` | 网关语音会话元数据（无聊天正文） |

编译二进制：

```bash
go build -buildvcs=false -o bin/server ./cmd/server
./bin/server
```

## Docker 部署

推荐使用 Docker Compose 部署。仓库根目录的 `docker-compose.yml` 已指向发布镜像：

```text
ghcr.io/space3044/chatgpt-web-voice:main
```

默认配置要点：

| 项 | 值 | 说明 |
|---|---|---|
| `network_mode` | `host` | 共用宿主机网络，出站走宿主机透明代理（如 dae） |
| 监听 | 宿主机 `8090` | host 网络下不再做端口映射 |
| `mem_limit` | `256m` | 容器内存上限（curl-impersonate 子进程略高于纯 Go） |
| `pull_policy` | `always` | 每次 up 尝试拉取最新镜像 |
| 上游传输 | `curl-impersonate` | 镜像内置 `curl_edge101`（与 ChatGPT2API-GO 一致） |
| 数据 | `./data` | SQLite 等 |
| 日志 | 容器 stdout | `docker compose logs -f` 查看 |

镜像默认 `VOICE_ENV=production`、不在容器内终止 TLS，上游默认 `VOICE_UPSTREAM_TRANSPORT=curl-impersonate`（二进制已打进 image，VPS 无需再装）。生产环境可在前面加 Nginx / Caddy / Traefik。请保证 `./data` 可写（进程用户为 uid 1000）。

本地 `go run` 仍默认 `tls-client`；只有 Docker 镜像默认 curl。

### 启动

```bash
cp .env.example .env
# 填写 VOICE_AUTH_USERNAME / VOICE_AUTH_PASSWORD / VOICE_TOKEN_ENCRYPTION_KEY
mkdir -p data
# 若权限报错：chown -R 1000:1000 data
docker compose up -d
```

启动后访问：`http://127.0.0.1:8090/voice`。

### 常用命令

```bash
docker compose ps
docker compose logs -f chatgpt-web-voice
docker compose pull
docker compose up -d
docker compose restart chatgpt-web-voice
docker compose down
```

### 环境变量

在 `.env` 中配置（Compose 会读取）：

| 变量 | 说明 |
|---|---|
| `VOICE_AUTH_USERNAME` | 管理端登录用户名（必填） |
| `VOICE_AUTH_PASSWORD` | 管理端登录密码（必填） |
| `VOICE_TOKEN_ENCRYPTION_KEY` | 密封账号 token 的 32 字节密钥（hex 或 base64，必填）；丢失后已存 token 无法解密 |
| `VOICE_UPSTREAM_TRANSPORT` | 上游传输：`curl-impersonate`（Docker 默认）/ `tls-client`（本地 go 默认）/ `go` |
| `VOICE_TLS_PROFILE` | tls-client 指纹档，默认 `chrome_120`（库无 edge 档时的回退） |
| `VOICE_IMPERSONATE` | curl 配置档；默认 `edge_101`（对齐 ChatGPT2API-GO） |
| `VOICE_CURL_IMPERSONATE_BIN` | curl 二进制路径；Docker 默认 `/app/bin/curl-impersonate/curl_edge101` |
| `VOICE_DEVICE_ID` / `VOICE_SESSION_ID` | 进程全局 `oai-device-id` / `oai-session-id`；未设置时启动生成 UUID |
| `VOICE_LOG_LEVEL` | 可选：`debug` / `info` / `warn` / `error` |

上游代理优先用账号池 per-account proxy；未配置时进程直连，由宿主机透明代理（dae 等）按规则处理。  
浏览器指纹为进程全局。Docker 镜像已捆绑 curl-impersonate，VPS 上 `docker compose pull && up` 即可，无需在宿主机安装 curl。

## 下游接口

下游使用在 `/keys` 创建的 Bearer API Key，**只能**访问 `/v1/*`。

```http
Authorization: Bearer vgw_live_<密钥>
```

### 接口列表

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/v1/health` | 健康检查 |
| `GET` | `/v1/voice/config` | 音色、语言、STUN、DataChannel 约定 |
| `POST` | `/v1/voice/sessions` | 建立或恢复语音会话（SDP 交换） |
| `POST` | `/v1/voice/sessions/{voice_session_id}/context` | 上报 DataChannel 学到的上游 id |
| `GET` | `/v1/voice/sessions/{voice_session_id}/title` | 拉取上游会话标题 |
| `DELETE` | `/v1/voice/sessions/{voice_session_id}` | 释放网关内存绑定 |

### 推荐接入流程

下游业务会话与网关 `voice_session_id` 一一对应。下游**只需保存** API Key 与 `voice_session_id`；池内账号与上游续聊线索由网关持久化，**不返回给下游**。

1. **首次建连**  
   `POST /v1/voice/sessions`，body 只带 `offer_sdp` 与语音选项。  
   保存响应中的 `voice_session_id` 到下游业务会话。

2. **通话中**  
   客户端自己维护 `RTCPeerConnection` 与 DataChannel。  
   学到上游 `conversation_id` 等后，调用  
   `POST /v1/voice/sessions/{voice_session_id}/context` 写入网关。

3. **挂断**  
   `DELETE /v1/voice/sessions/{voice_session_id}` 释放内存绑定；SQLite 元数据仍保留。

4. **恢复 / 再拨**  
   再次 `POST /v1/voice/sessions`，带上**同一个** `voice_session_id` 与新的 `offer_sdp`。  
   网关根据记录自动粘回账号并尽量续上上游会话。

### 请求示例

首次：

```bash
curl -X POST https://voice.example.com/v1/voice/sessions \
  -H "Authorization: Bearer $VOICE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "offer_sdp": "v=0\r\n...",
    "voice": "cove",
    "voice_mode": "wingman",
    "language_code": "auto"
  }'
```

恢复：

```bash
curl -X POST https://voice.example.com/v1/voice/sessions \
  -H "Authorization: Bearer $VOICE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "offer_sdp": "v=0\r\n...",
    "voice": "cove",
    "voice_mode": "wingman",
    "language_code": "auto",
    "voice_session_id": "vs_..."
  }'
```

响应（不会包含账号 id、token、代理等）：

```json
{
  "answer_sdp": "v=0\r\n...",
  "voice_session_id": "vs_...",
  "voice": "cove",
  "voice_mode": "wingman",
  "language_code": "auto"
}
```

### 责任划分

| 角色 | 负责 |
|---|---|
| 下游 | API Key 鉴权请求、WebRTC 媒体、DataChannel、字幕与业务会话内容存储 |
| 网关 | 鉴权、选号、SDP 代理、粘性账号、上游续聊元数据、会话记录 |
| chatgpt.com | 语音推理与远端媒体 |

`voice_session_id` 按 API Key 隔离；未知 id 返回 `404`。上游是否真正 resume 取决于 token 是否有效及 chatgpt.com 是否接受续聊参数（best-effort）。

## 架构

```text
下游 / 内置语音页
  mic + RTCPeerConnection + DataChannel(oai-events)
        │
        │  Authorization: Bearer / 管理端登录
        │  POST offer_sdp → answer_sdp
        ▼
Gateway (Go)
  鉴权 · 账号池 · SDP 代理 · 会话绑定 · SQLite 元数据
        │
        │  POST chatgpt.com/realtime/wm
        ▼
chatgpt.com + Azure WebRTC
  媒体面：客户端 ↔ 上游直连（不经网关）
```

### 核心模块

| 模块 | 职责 |
|---|---|
| `cmd/server` | 进程入口 |
| `internal/app` | 依赖装配、静态页、TLS |
| `internal/api` | HTTP 适配（管理端 + `/v1`） |
| `internal/auth` | 浏览器会话 / API Key |
| `internal/accounts` | 账号池（token 密封） |
| `internal/voice` | `/realtime/wm` 代理与会话绑定 |
| `internal/callsessions` | 网关会话元数据（无聊天正文） |
| `internal/conversations` | 内置语音页文本会话（管理端） |
| `internal/apikeys` | 下游 Key（仅存 hash） |

### 会话与恢复

- 网关为每次建连分配 `voice_session_id`（`vs_...`）。
- 成功建连后写入 `call_sessions`：调用方（admin / 下游 key）、粘性 `account_id`、上游 id、语音参数等。
- 挂断释放**内存绑定**；再拨时凭 `voice_session_id` 从 SQLite 恢复粘性账号与上游线索。
- 管理端 `/sessions` 可查看元数据，**不展示聊天内容**。下游 `/v1` 本身也不落库聊天正文。

更细的实现说明见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 许可与声明

MIT。研究 / 自托管网关用途。

本项目源码参考自 [dyhhhhhh/chatgpt-web-voice](https://github.com/dyhhhhhh/chatgpt-web-voice)，在其基础上继续开发与维护。感谢原作者的工作。

需要使用自有 ChatGPT Web 登录会话 token。  
与 OpenAI 无关联；请遵守 OpenAI 服务条款与当地法律法规。

- 参考源码：https://github.com/dyhhhhhh/chatgpt-web-voice
- 容器镜像：https://github.com/Space3044/chatgpt-web-voice/pkgs/container/chatgpt-web-voice
