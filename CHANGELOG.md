# Changelog

## Unreleased

- Upstream browser fingerprint is process-global (`VOICE_DEVICE_ID` / `VOICE_SESSION_ID` / UA / `Sec-Ch-Ua*`); removed per-account `device_id` from SQLite, APIs, and the accounts panel.
- Docker image bundles curl-impersonate (`v0.6.1`, amd64/arm64) on Debian glibc and defaults to `curl-impersonate` + `edge_101`; local `go run` still defaults to `tls-client` (`chrome_120`).
- Upstream browser persona aligned with ChatGPT2API-GO: Edge 143 UA/`Sec-Ch-Ua*` Client Hints, OAI client version/build, Accept-Language, Cache-Control/Pragma/Priority, and header order.
- Classifies Cloudflare HTML challenge pages on `/realtime/wm` without treating them as invalid tokens.
- Downstream `/v1` resume is gateway-owned: clients only keep `voice_session_id`; sticky pool account and upstream continuity are restored from `call_sessions` and are no longer returned or accepted on the public session create response.
- Hangup now releases the gateway in-memory voice binding after persisting sticky `account_id` / upstream ids; the next dial restores continuity from SQLite instead of keeping the binding alive for `VOICE_SESSION_TTL_SECONDS`.
- Added admin **会话记录** page (`/sessions`) and `call_sessions` metadata store: records who opened a voice session (`admin` vs downstream API key), sticky pool `account_id`, upstream ids, voice options, and active/released status — never chat content. Downstream `/v1` sessions get the same sticky resume via durable metadata after gateway restarts.
- Persist sticky pool `account_id` on local SQLite conversations (with upstream resume ids) so reconnects after a gateway restart re-select the same ChatGPT account instead of LRU-picking another; session create accepts/returns `account_id`, and a missing/disabled bound account fails closed rather than silently rotating.
- Fixed voice-page disconnect UI drift: transport `failed`/`disconnected` now fully tears down the call so the end button and composer leave the live/connecting state.
- Best-effort upstream conversation resume: sticky upstream `voice_session_id`, optional `conversation_id` / `parent_message_id` on `/realtime/wm`, gateway context update APIs, and SQLite fields on local conversations so reconnects can try to continue the same chatgpt.com thread (same account required; not guaranteed by upstream).
- Upstream conversation title fetch: gateway uses the sticky account token to `GET /backend-api/conversation/{id}` and returns only the title; built-in voice page auto-applies it to the local session title when available.
- Sealed ChatGPT `access_token` values at rest with AES-256-GCM (`VOICE_TOKEN_ENCRYPTION_KEY`); uniqueness and preferred-token lookups use a keyed `token_hash`, and startup rewrites legacy plaintext rows in place.
- Added hashed downstream API keys with one-time secret display, SQLite metadata, administrator CRUD APIs, and a `/keys` management panel.
- Added an API-key-only `/v1` integration surface for capability discovery, SDP session creation, and caller-owned session release without exposing account-pool data.
- Centralized voice, language, STUN, and negotiated DataChannel capabilities in the Go backend; the built-in browser client now consumes the same configuration as downstream clients.
- Removed unused account `refresh_token` storage, API fields, and accounts-panel UI; existing SQLite databases drop the column on startup.
- Restored process proxy environment fallback for upstream ChatGPT traffic: account proxy still wins when set; otherwise Go honors `HTTP_PROXY` / `HTTPS_PROXY` and `NO_PROXY` (including lowercase variants).
- Restructured internal packages into clearer layers: shared SQLite `store`, domain repositories (`accounts`, `conversations`), application services (`voice`), HTTP adapters (`api`), and composition root (`app`); `cmd/server` is now a thin entrypoint.
- Replaced shared Bearer gateway key with environment-injected username/password login, HttpOnly browser sessions, and Basic Auth for automation.
- Protected pages, static resources, and voice APIs behind the authentication middleware.
- Replaced runtime `accounts.json` loading with a SQLite account pool and added `cmd/migrate-accounts` for one-time migration.
- Added an authenticated account pool management panel with redacted account APIs for create, edit, enable/disable, search, and delete workflows.
- Redesigned the voice page as a chat workspace with a session history sidebar and a settings drawer.
- Added authenticated SQLite persistence for conversations and messages/captions, including one-time migration of legacy browser-local conversation history.
- Moved conversation management into the session sidebar context menu: rename, copy TXT, JSON export, and delete (with message cascade); removed standalone subtitle export/clear controls.
- Removed image upload and attachment handling from the gateway, conversation APIs, SQLite schema, and voice page.
- Added an optional “remember login” checkbox; unchecked logins use a browser-session cookie, while checked logins retain the HttpOnly cookie for the configured auth session TTL.
- Added structured `slog` JSON access/application logs with request IDs and sensitive-field redaction.
- Removed a separate global gateway proxy setting; upstream proxy is either per-account or the process environment (`HTTP_PROXY` / `HTTPS_PROXY`).
- Added explicit `development`/`production` runtime modes: development can generate a local self-signed certificate, while the production image serves internal HTTP for reverse-proxy TLS termination and never auto-generates certificates.
- Hard-fixed upstream TLS certificate verification in production; `VOICE_SKIP_SSL_VERIFY` remains a development-only troubleshooting setting and defaults to `false` everywhere.
- Added clean frontend routes at `/voice` and `/accounts`; former `.html` URLs now redirect to their canonical paths.
- Simplified browser authentication navigation: unauthenticated pages go to `/login`, and successful login always opens `/voice`.

## v0.2.0 - 2026-07-22

### Changed
- Ported gateway from Python/FastAPI to Go (stdlib `net/http`)
- Image dimension detection via Go `image` package (no Pillow)
- Binary deploy: single static binary + static assets

### Notes
- TLS fingerprint is standard Go crypto/tls (not curl_cffi Chrome impersonate)
- API surface and frontend protocol remain compatible with v0.1.0

## v0.1.0 - 2026-07-21

### Added
- Standalone FastAPI voice gateway (no chat2/yukkcat admin stack)
- `POST /api/voice/session` WebRTC SDP proxy to ChatGPT `/realtime/wm`
- `POST /api/voice/upload-image` for in-call image `file_id`
- `POST /api/voice/session/release` session/token unbind
- Browser client `static/voice.html`
  - realtime call
  - in-call text via `relay_message`
  - in-call image via sediment pointer
  - captions (`chat_message_delta`)
  - auto barge-in interrupt (`stop_speaking`)
- Docker + docker-compose one-command start
- Live demo: https://voice.peekcart.com/
