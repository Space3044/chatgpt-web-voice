FROM golang:1.22-alpine AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

COPY . .
RUN go mod tidy \
  && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/server ./cmd/server \
  && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/migrate-accounts ./cmd/migrate-accounts

FROM alpine:3.20

WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/server /app/server
COPY --from=builder /out/migrate-accounts /app/migrate-accounts
COPY static ./static
RUN mkdir -p /app/data

ENV VOICE_DATABASE_FILE=/app/data/voice.db
ENV VOICE_STATIC_DIR=/app/static
ENV VOICE_LISTEN_ADDR=:8090
ENV VOICE_ENV=production
ENV VOICE_TLS=false
ENV VOICE_SKIP_SSL_VERIFY=false
ENV VOICE_LOG_FORMAT=json
ENV VOICE_LOG_LEVEL=info

EXPOSE 8090
CMD ["/app/server"]
