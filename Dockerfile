# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS builder

WORKDIR /src
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/server ./cmd/server \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate-accounts ./cmd/migrate-accounts

# Official curl-impersonate Linux builds are glibc-linked; download the arch match.
FROM debian:bookworm-slim AS curl-impersonate
ARG TARGETARCH
ARG CURL_IMPERSONATE_VERSION=v0.6.1
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl \
  && rm -rf /var/lib/apt/lists/* \
  && case "${TARGETARCH}" in \
       amd64) curl_arch="x86_64-linux-gnu" ;; \
       arm64) curl_arch="aarch64-linux-gnu" ;; \
       *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
     esac \
  && asset="curl-impersonate-${CURL_IMPERSONATE_VERSION}.${curl_arch}.tar.gz" \
  && url="https://github.com/lwthiker/curl-impersonate/releases/download/${CURL_IMPERSONATE_VERSION}/${asset}" \
  && echo "downloading ${url}" \
  && curl -fsSL -o /tmp/curl-impersonate.tar.gz "${url}" \
  && mkdir -p /out \
  && tar -xzf /tmp/curl-impersonate.tar.gz -C /out \
  && rm -f /tmp/curl-impersonate.tar.gz \
  && chmod -R a+rX /out \
  && test -x /out/curl-impersonate-chrome \
  && test -x /out/curl_edge101

FROM debian:bookworm-slim

ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="chatgpt-web-voice" \
  org.opencontainers.image.description="Self-hosted ChatGPT.com Web Voice gateway" \
  org.opencontainers.image.source="https://github.com/Space3044/chatgpt-web-voice" \
  org.opencontainers.image.licenses="MIT" \
  org.opencontainers.image.version="${VERSION}" \
  org.opencontainers.image.revision="${REVISION}" \
  org.opencontainers.image.created="${BUILD_DATE}"

WORKDIR /app

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates tzdata wget bash zlib1g \
  && rm -rf /var/lib/apt/lists/* \
  && if ! getent group voice >/dev/null; then \
       if getent group 1000 >/dev/null; then \
         groupadd -r voice; \
       else \
         groupadd -g 1000 -r voice; \
       fi; \
     fi \
  && if ! id -u voice >/dev/null 2>&1; then \
       if getent passwd 1000 >/dev/null; then \
         useradd -g voice -r -M -d /app -s /usr/sbin/nologin voice; \
       else \
         useradd -u 1000 -g voice -r -M -d /app -s /usr/sbin/nologin voice; \
       fi; \
     fi \
  && mkdir -p /app/data /app/bin/curl-impersonate \
  && chown voice:voice /app/data

COPY --from=builder /out/server /app/server
COPY --from=builder /out/migrate-accounts /app/migrate-accounts
COPY --from=curl-impersonate /out/ /app/bin/curl-impersonate/
COPY static ./static

# Docker/VPS default: curl-impersonate (matches ChatGPT2API-GO runtime).
# Local `go run` still defaults to tls-client unless env is set.
ENV VOICE_DATABASE_FILE=/app/data/voice.db \
  VOICE_STATIC_DIR=/app/static \
  VOICE_LISTEN_ADDR=:8090 \
  VOICE_ENV=production \
  VOICE_TLS=true \
  VOICE_SKIP_SSL_VERIFY=false \
  VOICE_AUTH_SESSION_TTL_SECONDS=86400 \
  VOICE_LOGIN_MAX_FAILURES=8 \
  VOICE_LOGIN_WINDOW_SECONDS=900 \
  VOICE_LOGIN_LOCKOUT_SECONDS=900 \
  VOICE_SESSION_TTL_SECONDS=180 \
  VOICE_MAX_ACCOUNT_ATTEMPTS=4 \
  VOICE_LOG_FORMAT=json \
  VOICE_LOG_LEVEL=info \
  VOICE_UPSTREAM_TRANSPORT=curl-impersonate \
  VOICE_IMPERSONATE=edge_101 \
  VOICE_CURL_IMPERSONATE_BIN=/app/bin/curl-impersonate/curl_edge101 \
  PATH=/app/bin/curl-impersonate:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

USER voice:voice
EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -T 3 -O /dev/null "http://127.0.0.1:8090/login" || exit 1

CMD ["/app/server"]
