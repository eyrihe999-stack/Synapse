---
name: deploy
description: 一键构建前后端 Docker 镜像，推送到本地私有仓库并部署。支持指定服务和版本号。
disable-model-invocation: true
allowed-tools: Bash(docker *), Bash(docker-compose *), Bash(curl *), Bash(git *), Read
---

ultrathink

将工程打包为 Docker 镜像，推送到本地私有 Registry（localhost:5001），并部署到本地 Docker 环境。

- 后端服务名：`synapse`，镜像名 `synapse`，版本变量 `SERVER_TAG`
- 前端服务名：`synapse-web`，镜像名 `synapse-web`，版本变量 `WEB_TAG`

## 参数解析

参数可以包含**服务范围**和**版本号**，顺序不限，空格分隔：

- `/deploy` — 全部服务 + 自动版本号
- `/deploy web` — 仅前端 + 自动版本号
- `/deploy server` — 仅后端 + 自动版本号
- `/deploy v1.0.0` — 全部服务 + 指定版本号
- `/deploy web v1.0.0` — 仅前端 + 指定版本号
- `/deploy server latest` — 仅后端 + latest 标签

解析规则：
- 参数中包含 `web` → TARGETS=synapse-web，TAG_VAR=WEB_TAG
- 参数中包含 `server` → TARGETS=synapse，TAG_VAR=SERVER_TAG
- 都不包含 → TARGETS="synapse synapse-web"，两个 TAG_VAR 都设置
- 参数中既不是 `web` 也不是 `server` 的部分视为 IMAGE_TAG
- 没有 IMAGE_TAG 则自动生成

## 步骤 1：前置检查

1. 执行 `docker info` 确认 Docker 守护进程正在运行，如果未运行则提示用户先启动 Docker Desktop。
2. 如果 TARGETS 包含 synapse：确认项目根目录存在 `Dockerfile`。
3. 如果 TARGETS 包含 synapse-web：确认 `../Synapse-Web/Dockerfile` 存在。
4. 确认 `docker-compose.yml` 存在。
5. 执行 `curl -sf http://localhost:5001/v2/` 确认本地 Registry 可用。

## 步骤 2：确定版本号

如果用户传入了版本号参数，直接使用。

如果没有传入，自动生成：
```bash
GIT_SHA=$(git rev-parse --short HEAD)
TIMESTAMP=$(date +%Y%m%d%H%M%S)
IMAGE_TAG="${TIMESTAMP}-${GIT_SHA}"
```
如果工作区有未提交改动（`git status --porcelain` 非空），tag 后追加 `-dirty`。

将 TARGETS 和 IMAGE_TAG 告知用户。

## 步骤 3：准备网络

```bash
docker network create synapse-net 2>/dev/null || true
docker network connect synapse-net synapse-mysql 2>/dev/null || true
docker network connect synapse-net redis 2>/dev/null || true
```

## 步骤 4：停止旧容器

仅停止 TARGETS 中指定的服务：
```bash
docker compose stop ${TARGETS}
docker compose rm -f ${TARGETS}
```

## 步骤 5：构建镜像

仅构建 TARGETS 中的服务，使用对应的 TAG 变量：

- 仅后端：`SERVER_TAG=${IMAGE_TAG} docker compose build --no-cache synapse`
- 仅前端：`WEB_TAG=${IMAGE_TAG} docker compose build --no-cache synapse-web`
- 全部：`SERVER_TAG=${IMAGE_TAG} WEB_TAG=${IMAGE_TAG} docker compose build --no-cache`

如果构建失败，输出错误日志并停止。

## 步骤 6：推送到本地 Registry

仅推送 TARGETS 中的镜像。对每个目标服务执行：
```bash
docker push localhost:5001/${SERVICE}:${IMAGE_TAG}
docker tag localhost:5001/${SERVICE}:${IMAGE_TAG} localhost:5001/${SERVICE}:latest
docker push localhost:5001/${SERVICE}:latest
```

## 步骤 7：启动服务

**重要**：compose.yml 里 `image: localhost:5001/synapse:${SERVER_TAG:-latest}`。如果单服务部署时不设置另一个服务的 TAG 环境变量，compose 会把它解析为 `:latest`；若当前运行的容器是用显式 tag 启动的，compose 视为 config 变更，会连带 recreate 那个服务，并把它重新绑到 `:latest`。

所以单服务部署前，必须先读取另一个服务当前运行的镜像 tag，并显式传回，保持它固定在原版本。

```bash
# 读取当前运行容器的 tag（失败则回落 latest）
get_tag() {
  docker inspect -f '{{.Config.Image}}' "$1" 2>/dev/null | awk -F: '{print $NF}' || echo latest
}
```

- 仅后端：
  ```bash
  WEB_TAG_KEEP=$(get_tag synapse-synapse-web-1)
  SERVER_TAG=${IMAGE_TAG} WEB_TAG=${WEB_TAG_KEEP} docker compose up -d synapse
  ```
- 仅前端：
  ```bash
  SERVER_TAG_KEEP=$(get_tag synapse-synapse-1)
  SERVER_TAG=${SERVER_TAG_KEEP} WEB_TAG=${IMAGE_TAG} docker compose up -d synapse-web
  ```
- 全部：
  ```bash
  SERVER_TAG=${IMAGE_TAG} WEB_TAG=${IMAGE_TAG} docker compose up -d
  ```

这样另一个服务的镜像引用与其运行中的容器完全一致，compose 不会 recreate 它。

## 步骤 8：健康检查

1. 等待服务启动（最多 30 秒），使用 `docker compose ps` 检查容器状态。
2. 如果 TARGETS 包含 synapse：`curl -sf http://localhost:8080/health`
3. 如果 TARGETS 包含 synapse-web：`curl -sf http://localhost:3080`
4. 如果健康检查失败，输出 `docker compose logs --tail 50 ${TARGETS}` 帮助排查。

## 步骤 9：输出结果

部署成功后输出：
- 部署的服务和镜像版本
- 各服务运行状态（`docker compose ps`）
- 后端地址（如部署了）：`http://localhost:8080`
- 前端地址（如部署了）：`http://localhost:3080`
- 仓库中的历史版本
- 常用命令提示：
  - 查看日志：`docker compose logs -f`
  - 回滚后端：`SERVER_TAG=xxx docker compose up -d synapse`
  - 回滚前端：`WEB_TAG=xxx docker compose up -d synapse-web`
  - 停止服务：`docker compose down`
  - 查看仓库所有镜像：`curl -s http://localhost:5001/v2/_catalog`
