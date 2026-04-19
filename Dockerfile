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

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build(CGO 开启 + 静态链接)
# 首次 build 会花几分钟编译 6 个 tree-sitter grammar C 源;Docker layer 缓存后续增量构建只重编改过的 Go 代码。
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w -extldflags '-static'" -o /bin/synapse ./cmd/synapse/

# ── Runtime stage ─────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/synapse /app/synapse
COPY config/ /app/config/

EXPOSE 8080

ENTRYPOINT ["/app/synapse"]
