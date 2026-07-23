# Changelog

## Unreleased

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
