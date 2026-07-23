FROM golang:1.22-alpine AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/server ./cmd/server \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate-accounts ./cmd/migrate-accounts

FROM alpine:3.20

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

RUN apk add --no-cache ca-certificates tzdata wget \
  && addgroup -S -g 1000 voice \
  && adduser -S -u 1000 -G voice -H -s /sbin/nologin voice \
  && mkdir -p /app/data \
  && chown voice:voice /app/data

COPY --from=builder /out/server /app/server
COPY --from=builder /out/migrate-accounts /app/migrate-accounts
COPY static ./static

ENV VOICE_DATABASE_FILE=/app/data/voice.db \
  VOICE_STATIC_DIR=/app/static \
  VOICE_LISTEN_ADDR=:8090 \
  VOICE_ENV=production \
  VOICE_TLS=true \
  VOICE_SKIP_SSL_VERIFY=false \
  VOICE_AUTH_SESSION_TTL_SECONDS=43200 \
  VOICE_LOGIN_MAX_FAILURES=8 \
  VOICE_LOGIN_WINDOW_SECONDS=900 \
  VOICE_LOGIN_LOCKOUT_SECONDS=900 \
  VOICE_SESSION_TTL_SECONDS=180 \
  VOICE_MAX_ACCOUNT_ATTEMPTS=4 \
  VOICE_LOG_FORMAT=json \
  VOICE_LOG_LEVEL=info

USER voice:voice
EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -Y off -T 3 -O /dev/null "http://127.0.0.1:8090/login" || exit 1

CMD ["/app/server"]
