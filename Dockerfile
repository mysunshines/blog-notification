# =============================================================================
# notification-service 多阶段构建
# =============================================================================
# syntax=docker/dockerfile:1

FROM golang:1.25.0-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct
ENV GOSUMDB=off

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG GIT_VERSION=dev

ARG APP_NAME=notification-service
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags "-X main.Version=${GIT_VERSION}" \
    -o /app/${APP_NAME} ./cmd/server

FROM alpine:latest

ENV APP_NAME=notification-service

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/${APP_NAME} .
COPY --from=builder /app/config/ ./config/

RUN adduser -D -g '' appuser
USER appuser

# 8085: HTTP(WebSocket+探活), 9105: gRPC 业务通信, 9099: Prometheus Metrics
EXPOSE 8085 9105 9099

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8085/health || exit 1

CMD exec ./${APP_NAME}
