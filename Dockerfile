# syntax=docker/dockerfile:1.7
# ── Build stage ────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# build-base / gcc / musl-dev:tree-sitter(pkg/codechunker)走 CGO,需要 C toolchain 编译各语言 grammar 的 C 代码。
# 完整 static 链接:-extldflags "-static" 让 runtime image 不用装任何 libc 兼容包。
RUN apk add --no-cache git ca-certificates tzdata build-base

WORKDIR /src

# 国内网络走 goproxy.cn;容器内默认的 proxy.golang.org 在国内不通。
# 如果部署在海外环境,可以在 docker compose build 时传 --build-arg GOPROXY=direct 覆盖。
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# cache mount 让 mod 下载与 build cache 跨构建持久化,避免 COPY . . 层失效时 tree-sitter C 源全量重编
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
# /deploy skill 在 build 前 export GIT_SHA,通过 compose build arg 传进来;没传默认 unknown
ARG GIT_SHA=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w -X main.GitSHA=${GIT_SHA} -extldflags '-static'" -o /bin/synapse ./cmd/synapse/

# ── Runtime stage ─────────────────────────────────────────────
FROM alpine:3.23

# wget:HEALTHCHECK 探活用;app user:非 root 跑 binary
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app

COPY --from=builder --chown=app:app /bin/synapse /app/synapse
# config.local.yaml 在 .dockerignore 里排除,不烘进镜像;由 compose 挂载到 /app/config/config.local.yaml
COPY --chown=app:app config/ /app/config/

# 本地部署默认读 config.local.yaml(gitignored 真秘钥),compose 会挂载进来
ENV APP_ENV=local

USER app

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD wget -q -O /dev/null http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/synapse"]
