# chatgpt-web-voice

[![Live Demo](https://img.shields.io/badge/demo-voice.peekcart.com-10a37f)](https://voice.peekcart.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev/)

Self-hosted **ChatGPT.com Web Voice** gateway. The browser owns WebRTC media; this service owns the account pool, SDP proxy to `chatgpt.com/realtime/wm`, voice-session binding, and persisted text conversations.

Use a ChatGPT web `access_token` from your own account pool. No official Realtime API key is required for this web path. Upstream credentials are sealed in SQLite (AES-256-GCM) and are never returned to the browser in full.

## Features

- Realtime voice call (`/realtime/wm` + WebRTC + DataChannel)
- In-call text via DataChannel `relay_message`
- Auto barge-in interrupt (`action_request: stop_speaking`)
- Captions from `chat_message_delta`
- Voice session token binding (`voice_session_id`)
- Authenticated SQLite account pool panel
- AES-256-GCM sealed ChatGPT access tokens at rest (`VOICE_TOKEN_ENCRYPTION_KEY`)
- Hashed downstream API keys with one-time secret display
- API-key-only `/v1` SDP connection API and public voice capability document
- JWT expiry display for stored access tokens
- Manual account probe against `backend-api/settings/user`
- Chat-style voice workspace with session history and settings drawer
- Authenticated SQLite persistence for conversations and captions/messages

WebRTC media flows between the browser and the upstream service. The gateway does not receive or store raw call audio; it persists conversation metadata and text/captions only.

## Start locally

```bash
cp .env.example .env
# set VOICE_AUTH_USERNAME, VOICE_AUTH_PASSWORD, and VOICE_TOKEN_ENCRYPTION_KEY
# VOICE_TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)
```

`VOICE_ENV` defaults to `development` and only accepts `development` or `production`. Development may enable gateway TLS for LAN microphone use via `./scripts/dev.sh --tls` (self-signed cert under `data/certs/`).

```bash
set -a
source .env
set +a
go run ./cmd/server
# open http://127.0.0.1:8090/voice
```

Or with the WSL helper (loads `.env` automatically):

```bash
bash ./scripts/dev.sh
```

Log in with the configured username/password. Manage ChatGPT web credentials at:

```text
http://127.0.0.1:8090/accounts
```

The accounts panel is also linked from the voice page settings drawer.

Create and revoke downstream API keys at:

```text
http://127.0.0.1:8090/keys
```

### Optional: legacy JSON import

If you still have an old `accounts.json`, import it once. The running server never reads that file.

```bash
# requires VOICE_TOKEN_ENCRYPTION_KEY in the environment
go run ./cmd/migrate-accounts \
  -from ./data/accounts.json \
  -database ./data/voice.db
```

New installs can skip this. The server creates an empty SQLite database and accounts can be added from the panel.

### Build a binary

```bash
go build -buildvcs=false -o bin/server ./cmd/server
./bin/server
```

## Docker Compose

```bash
cp .env.example .env
# set VOICE_AUTH_USERNAME, VOICE_AUTH_PASSWORD, and VOICE_TOKEN_ENCRYPTION_KEY
mkdir -p data
docker compose up --build -d
docker compose ps
```

Open `http://127.0.0.1:8090/voice` by default. Change the published host port with `VOICE_PORT`.

The production image sets `VOICE_ENV=production` and `VOICE_TLS=false`. Serve HTTPS at Nginx, Traefik, Caddy, or another reverse proxy. SQLite data lives in `./data`; static assets are baked into the image (rebuild after frontend changes). Compose requires `VOICE_AUTH_USERNAME`, `VOICE_AUTH_PASSWORD`, and `VOICE_TOKEN_ENCRYPTION_KEY`.

```bash
docker compose logs -f chatgpt-web-voice
docker compose restart chatgpt-web-voice
docker compose down
```

Legacy JSON import under Compose:

```bash
docker compose run --rm --build chatgpt-web-voice \
  /app/migrate-accounts -from /app/data/accounts.json -database /app/data/voice.db
```

Production mode never auto-generates a certificate. If you set `VOICE_TLS=true` outside the standard Compose file, you must also provide `VOICE_TLS_CERT` and `VOICE_TLS_KEY`.

## WSL2 + Windows / VS Code

Run the service inside WSL and open it from the Windows browser.

### Recommended: VS Code Port Forward

1. Open this folder with **VS Code Remote - WSL**
2. In WSL:

```bash
set -a
source .env
set +a
./scripts/dev.sh
```

3. VS Code **Ports** → **Forward Port** → `8090`
4. On Windows and WSL use:

```text
http://127.0.0.1:8090/voice
```

`127.0.0.1` is a secure context, so the microphone works. Do not use a LAN IP over plain HTTP for voice; the page may load but mic capture will fail.

### Alternative: TLS on LAN IP

```bash
./scripts/dev.sh --tls
# open https://<WSL-IP>:8090/voice  (accept self-signed warning)
```

Development convenience only. Production should keep the gateway on internal HTTP and terminate TLS at the reverse proxy.

### Checklist

| Check | Action |
|---|---|
| Server up in WSL | `curl -u "$VOICE_AUTH_USERNAME:$VOICE_AUTH_PASSWORD" -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/voice` → `200` |
| Port forwarded | Ports panel shows `8090` → `localhost:8090` |
| Browser URL | `http://127.0.0.1:8090/voice` (not HTTPS unless TLS is enabled) |
| Mic fails on LAN IP | Use forwarded `127.0.0.1` or `./scripts/dev.sh --tls` |

## Architecture

```text
Built-in browser (static/voice.html)
  mic + RTCPeerConnection + DataChannel(oai-events)
        |
        | POST /api/voice/session
        v
Gateway (Go, stdlib net/http)
  administrator auth + downstream API key auth
  SQLite account pool + per-account proxy + hashed API keys
  header shaping for ChatGPT web requests
        |
        v
chatgpt.com
  /realtime/wm  +  Azure WebRTC media

Downstream backend
  Authorization: Bearer vgw_live_...
  GET /v1/voice/config
  POST /v1/voice/sessions
        |
        +--> returns answer SDP; downstream owns WebRTC, captions, and history
```

Internal package layout:

```text
cmd/server              process entrypoint
internal/app            wiring, HTTP root, TLS, static routes
internal/api            HTTP handlers (depends on domain interfaces)
internal/auth           login sessions and Basic Auth
internal/apikeys        hashed downstream credentials
internal/voice          realtime session + account probe service
internal/accounts       ChatGPT account pool repository
internal/conversations  conversation / caption repository
internal/store          shared SQLite open + schema
internal/config         environment configuration
internal/httpclient     upstream HTTP transport policy
internal/logging        slog + request middleware
internal/tokenutil      JWT exp display helpers
internal/tlsutil        local self-signed cert helper
```

Upstream HTTP uses Go `net/http` and `crypto/tls`. Requests set browser-like headers (`User-Agent`, `Origin`, `Referer`, `oai-device-id`, client version fields). This is not Chrome TLS fingerprint impersonation.

## Authentication

All pages, static assets, and business APIs require login. Public routes:

- `GET /login`
- `POST /api/auth/login`

Successful browser login always redirects to `/voice`.

Browsers use an HttpOnly session cookie. The login page can optionally keep that cookie for `VOICE_AUTH_SESSION_TTL_SECONDS`. Session tokens live in gateway memory; restarting the process requires logging in again.

Automation can use HTTP Basic Auth:

```bash
curl -u "$VOICE_AUTH_USERNAME:$VOICE_AUTH_PASSWORD" \
  http://127.0.0.1:8090/api/voice/health
```

```http
Authorization: Basic <base64(username:password)>
```

Downstream integrations use a separately generated Bearer API key. API keys
can access `/v1/*` only; they cannot access pages, account management,
conversations, or administrator APIs. Keep long-lived keys in the downstream
backend rather than browser JavaScript.

```http
Authorization: Bearer vgw_live_<random-secret>
```

## API

| Method | Path | Description |
|---|---|---|
| GET | `/api/voice/health` | health |
| GET | `/api/voice/config` | non-secret voice/WebRTC capabilities for the built-in client |
| POST | `/api/voice/session` | offer SDP → answer SDP |
| POST | `/api/voice/session/release` | unbind voice session |
| GET | `/api/accounts` | list accounts and pool stats (secrets redacted; includes JWT expiry fields) |
| POST | `/api/accounts` | create account |
| PUT | `/api/accounts/{id}` | update / enable / disable account |
| DELETE | `/api/accounts/{id}` | delete account |
| POST | `/api/accounts/{id}/check` | probe token via `GET /backend-api/settings/user` |
| GET | `/api/keys` | list redacted downstream API key metadata |
| POST | `/api/keys` | create a key and return its secret once |
| PATCH | `/api/keys/{id}` | rename, enable, or disable a key |
| DELETE | `/api/keys/{id}` | permanently revoke and delete a key |
| GET | `/api/conversations` | list conversations for the logged-in user |
| POST | `/api/conversations` | create conversation |
| GET | `/api/conversations/{id}` | read conversation and messages |
| PATCH | `/api/conversations/{id}` | rename conversation |
| DELETE | `/api/conversations/{id}` | delete conversation and messages |
| POST | `/api/conversations/{id}/messages` | insert or update a text message/caption |
| GET | `/api/auth/session` | current authenticated user |
| POST | `/api/auth/logout` | revoke browser session |

### Voice session body

Only signaling and voice options are accepted. Downstream clients must not send `access_token`, `device_id`, or `proxy`.

```json
{
  "offer_sdp": "v=0\r\n...",
  "voice": "cove",
  "voice_mode": "wingman",
  "language_code": "auto",
  "voice_session_id": ""
}
```

### Downstream connection API

The gateway only authenticates the caller, selects an internal account, and
exchanges SDP. The downstream owns `RTCPeerConnection`, microphone/audio,
DataChannel events, captions, business conversations, and persistence.

| Method | Path | Description |
|---|---|---|
| GET | `/v1/health` | authenticated downstream API health |
| GET | `/v1/voice/config` | voices, languages, defaults, STUN, and DataChannel configuration |
| POST | `/v1/voice/sessions` | offer SDP → answer SDP |
| DELETE | `/v1/voice/sessions/{voice_session_id}` | release the caller-owned gateway binding |

```bash
curl -X POST https://voice.example.com/v1/voice/sessions \
  -H "Authorization: Bearer $VOICE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "offer_sdp": "v=0\\r\\n...",
    "voice": "cove",
    "voice_mode": "wingman",
    "language_code": "auto"
  }'
```

```json
{
  "answer_sdp": "v=0\r\n...",
  "voice_session_id": "vs_...",
  "voice": "cove",
  "voice_mode": "wingman",
  "language_code": "auto"
}
```

The response never includes account IDs, email addresses, access tokens,
device IDs, proxies, account status, or pool statistics. A voice session is
namespaced to the API key that created it, so another key cannot reuse or
release that binding. Omit `voice_session_id` when creating the first
connection; a later reconnect may send the ID previously returned by the same
API key. Unknown IDs return `404`, while unknown voice, mode, or language
values return `400`.

### Account probe

`POST /api/accounts/{id}/check` calls ChatGPT `GET /backend-api/settings/user` with the stored token:

| Upstream result | Gateway behavior |
|---|---|
| `200` + JSON | report `alive` |
| `401` | report `unauthorized`, mark account disabled in SQLite |
| HTML challenge / network / other | report `unknown`, do **not** disable the account |

Account list responses also include local JWT claims when the access token is a JWT: `token_has_exp`, `token_exp`, `expires_in_seconds`, `token_expired`. Signature is not verified; this is display and pre-check only.

## In-call protocol

Envelope:

```json
{ "type": "data_message", "data": "<json string>" }
```

Text:

```json
{
  "type": "relay_message",
  "payload": {
    "type": "relay_message",
    "message": {
      "id": "uuid",
      "author": { "role": "user" },
      "create_time": 1710000000.0,
      "content": { "content_type": "text", "parts": ["hello"] },
      "metadata": { "serialization_metadata": { "custom_symbol_offsets": [] } },
      "clientMetadata": { "isOptimistic": true }
    }
  }
}
```

Interrupt:

```json
{ "type": "action_request", "payload": { "action": "stop_speaking" } }
```

## SQLite data

Runtime database: `VOICE_DATABASE_FILE` (default under `data/voice.db`). Conversation text is stored as plaintext. ChatGPT `access_token` values are sealed with AES-256-GCM using `VOICE_TOKEN_ENCRYPTION_KEY` (32-byte hex or base64); uniqueness uses a keyed `token_hash` column. Downstream API key secrets store only SHA-256 hashes and display prefixes. The database file is created with owner-only permissions where supported.

Generate a stable encryption key once (`openssl rand -hex 32`) and back it up with the same care as the login password. Losing the key makes existing sealed account tokens unreadable. On startup the gateway rewrites any legacy plaintext `access_token` rows in place.

Selection prefers the least recently used enabled account. An upstream **401** from the voice path or from a manual probe disables that account in SQLite.

Proxy selection for upstream ChatGPT requests:

1. If the selected account has a proxy in SQLite, that proxy is used.
2. Otherwise Go follows the process proxy environment (`HTTP_PROXY` / `HTTPS_PROXY` and `NO_PROXY`; lowercase variants are also recognized).
3. If neither is set, the request goes direct.

There is no gateway-wide `VOICE_*` proxy setting. These are standard process environment variables, and the account proxy always takes precedence. Docker Compose passes through the host shell's standard proxy variables when present. Windows system proxy/PAC settings are not automatically visible inside WSL or a container unless they are exported into that process environment.

The `/accounts` panel supports create, edit, enable/disable, search, delete, JWT expiry display, and manual probe. Empty secret fields keep existing values on edit; proxy has an explicit clear action.

The `/keys` panel supports create, one-time secret copy, rename, enable/disable,
and delete. Disabling or deleting a key blocks subsequent `/v1` requests. Since
media flows directly between the downstream and ChatGPT after SDP exchange,
revoking a key does not forcibly terminate an already established peer
connection.

List/write APIs never return full access tokens or proxy passwords. They expose an access-token preview, a password-free proxy preview, and JWT expiry metadata.

## Logging

JSON logs by default. HTTP access events include request ID, method, path, status, response bytes, duration, remote IP, and user agent. Responses include `X-Request-ID`. Passwords, authorization headers, and ChatGPT tokens are not logged.

## Environment

| Env | Default | Meaning |
|---|---|---|
| `VOICE_AUTH_USERNAME` | required | gateway login username |
| `VOICE_AUTH_PASSWORD` | required | gateway login password |
| `VOICE_TOKEN_ENCRYPTION_KEY` | required | 32-byte key (hex or base64) that seals account access tokens at rest |
| `VOICE_PORT` | `8090` | Docker Compose host port |
| `VOICE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

The standard Compose container uses SQLite at `/app/data/voice.db`, static assets at `/app/static`, listener `:8090`, `VOICE_ENV=production`, `VOICE_TLS=false`, and JSON logs.

In production, upstream TLS verification is always on (`VOICE_SKIP_SSL_VERIFY` is forced false). Local development may set additional knobs such as `VOICE_ENV`, `VOICE_TLS`, `VOICE_DATABASE_FILE`, and `VOICE_LOG_FORMAT` outside Compose.

## Project layout

```text
cmd/server/              gateway entrypoint
cmd/migrate-accounts/    one-time JSON → SQLite migration
internal/auth/           browser session + Basic Auth
internal/apikeys/        SQLite API key hashes and metadata
internal/secretbox/      AES-GCM seal/open for account access tokens
internal/config/         environment config
internal/accounts/       SQLite accounts and conversations
internal/logging/        slog setup and HTTP middleware
internal/httpclient/     upstream HTTP client (account proxy / process proxy environment / skip-verify)
internal/tokenutil/      JWT expiry helpers
internal/voice/          /realtime/wm gateway and account probe
internal/api/            protected HTTP handlers
static/login.html        login page
static/voice.html        voice client
static/accounts.html     account pool panel
static/keys.html         downstream API key panel
data/voice.db            runtime database (gitignored)
```

## Security

- Do not commit `voice.db`, credential dumps, tokens, or `VOICE_TOKEN_ENCRYPTION_KEY`
- ChatGPT `access_token` values are sealed in SQLite; keep the encryption key offline-backed up
- The browser only holds a gateway session cookie; it never receives an upstream web token
- Downstream API keys are shown once, stored as hashes, and scoped to `/v1/*`
- Use HTTPS whenever the login page is reachable beyond localhost (for example Caddy reverse proxy)
- Keep gateway credentials strong and private

## License / disclaimer

MIT. Research / self-hosted gateway.

Requires your own ChatGPT web login session token.  
Not affiliated with OpenAI. Follow OpenAI ToS and local laws.

## Links

- Live demo: https://voice.peekcart.com/
