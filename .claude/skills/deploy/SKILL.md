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

参数可以包含**服务范围**、**版本号**和 **`keep` 标记**，顺序不限，空格分隔：

- `/deploy` — 全部服务 + 自动版本号 + **部署后自动清旧 tag**
- `/deploy web` — 仅前端 + 自动版本号 + 自动清旧 tag
- `/deploy server` — 仅后端 + 自动版本号 + 自动清旧 tag
- `/deploy v1.0.0` — 全部服务 + 指定版本号 + 自动清旧 tag
- `/deploy web v1.0.0` — 仅前端 + 指定版本号 + 自动清旧 tag
- `/deploy server latest` — 仅后端 + latest 标签 + 自动清旧 tag
- `/deploy keep` — 全部服务 + 自动版本号 + **保留所有历史 tag**
- `/deploy server keep` — 仅后端 + 自动版本号 + 保留历史

解析规则：
- 参数中包含 `web` → TARGETS=synapse-web，TAG_VAR=WEB_TAG
- 参数中包含 `server` → TARGETS=synapse，TAG_VAR=SERVER_TAG
- 都不包含 → TARGETS="synapse synapse-web"，两个 TAG_VAR 都设置
- 参数中包含 `keep` → KEEP_HISTORY=1(保留旧 tag),否则 KEEP_HISTORY=0(部署成功后自动清)
- 参数中既不是 `web`/`server`/`keep` 的部分视为 IMAGE_TAG
- 没有 IMAGE_TAG 则自动生成

## 步骤 1：前置检查

1. 执行 `docker info` 确认 Docker 守护进程正在运行，如果未运行则提示用户先启动 Docker Desktop。
2. 如果 TARGETS 包含 synapse：确认项目根目录存在 `Dockerfile`。
3. 如果 TARGETS 包含 synapse-web：确认 `../Synapse-Web/Dockerfile` 存在。
4. 确认 `docker-compose.yml` 存在。
5. 执行 `curl -sf http://localhost:5001/v2/` 确认本地 Registry 可用。
6. 如果 TARGETS 包含 synapse：确认 `config/config.local.yaml` 存在(gitignored,放真秘钥;compose 以只读挂载进容器,APP_ENV=local 让 loader 读它)。缺失时停止并提示用户照 `config.dev.yaml` 模板补一份。

## 步骤 2：确定版本号 + GIT_SHA

先取 `GIT_SHA`(后端 build 时通过 `-X main.GitSHA=` 注入 binary,启动日志里会打出):

```bash
GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
```

再确定 `IMAGE_TAG`:用户传入的优先,否则按 `${TIMESTAMP}-${GIT_SHA}[+-dirty]` 生成:

```bash
if [ -z "${IMAGE_TAG}" ]; then
  TIMESTAMP=$(date +%Y%m%d%H%M%S)
  IMAGE_TAG="${TIMESTAMP}-${GIT_SHA}"
  [ -n "$(git status --porcelain 2>/dev/null)" ] && IMAGE_TAG="${IMAGE_TAG}-dirty"
fi
```

将 TARGETS、IMAGE_TAG、GIT_SHA 告知用户。

## 步骤 3：准备网络

基础设施容器(mysql/pg/redis/redisinsight)启动时已加入 `synapse-net`,这里只确保网络存在:

```bash
docker network create synapse-net 2>/dev/null || true
```

## 步骤 4：停止旧容器

仅停止 TARGETS 中指定的服务：
```bash
docker compose stop ${TARGETS}
docker compose rm -f ${TARGETS}
```

## 步骤 5：构建镜像

仅构建 TARGETS 中的服务,同时传入 TAG 和 `GIT_SHA`(后端 Dockerfile 的 `ARG GIT_SHA` 消费它写进 binary;前端忽略):

- 仅后端:`SERVER_TAG=${IMAGE_TAG} GIT_SHA=${GIT_SHA} docker compose build synapse`
- 仅前端:`WEB_TAG=${IMAGE_TAG} docker compose build synapse-web`
- 全部:`SERVER_TAG=${IMAGE_TAG} WEB_TAG=${IMAGE_TAG} GIT_SHA=${GIT_SHA} docker compose build`

**不加 `--no-cache`**:BuildKit 会按 COPY 文件 hash 自动让变动的层失效,代码改了自然重编;tag 里已带 git sha + timestamp + dirty 标记,不会拿到脏镜像。强制全量重建时再手动 `--no-cache`(例如怀疑基础镜像更新或 cache mount 污染)。

构建失败时抓最近日志排查:`docker compose logs --tail 50 ${TARGETS}`。

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
# 读取当前运行容器的 tag(容器不存在或异常时回落 latest)
get_tag() {
  local img
  img=$(docker inspect -f '{{.Config.Image}}' "$1" 2>/dev/null) || { echo latest; return; }
  [ -z "$img" ] && echo latest || echo "${img##*:}"
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

30 秒内轮询 TARGETS 对应端点,任一服务不就绪则 tail compose 日志帮助排查:

```bash
case " $TARGETS " in *" synapse "*) NEED_S=1;; *) NEED_S=0;; esac
case " $TARGETS " in *" synapse-web "*) NEED_W=1;; *) NEED_W=0;; esac
OK_S=$((1 - NEED_S)); OK_W=$((1 - NEED_W))
for i in $(seq 1 30); do
  [ "$OK_S" = 0 ] && curl -sf --max-time 3 http://localhost:8080/health >/dev/null 2>&1 && OK_S=1
  [ "$OK_W" = 0 ] && curl -sf --max-time 3 http://localhost:3080 >/dev/null 2>&1 && OK_W=1
  [ "$OK_S" = 1 ] && [ "$OK_W" = 1 ] && break
  sleep 1
done
if [ "$OK_S" != 1 ] || [ "$OK_W" != 1 ]; then
  echo "health check failed, recent logs:"
  docker compose logs --tail 50 ${TARGETS}
fi
```

两个 Dockerfile 自带 `HEALTHCHECK`(wget 探活应用端点),`docker compose ps` 也能看 healthy/unhealthy;curl 轮询是更直接的部署闸门,保留。

## 步骤 9：清理旧 tag(KEEP_HISTORY=0 时执行)

默认部署成功后清掉 TARGETS 对应服务的历史 tag,**保留本次 IMAGE_TAG + `:latest` + 上一次 IMAGE_TAG**(一步回滚窗口)。分两段:先清本地 docker image store,再清 Registry 端 blob(两周就能堆到 70GB)。

传 `keep` 时整块跳过。

### 9a. 清本地 docker image store

```bash
if [ "${KEEP_HISTORY}" != "1" ]; then
  for svc in ${TARGETS}; do
    # 按 CreatedAt 倒序列出该 service 的所有 tag,剔除 :latest 和本次,
    # 保留最新 1 条(上一次部署)作为回滚位,其余删除
    docker images --format '{{.CreatedAt}}|{{.Repository}}:{{.Tag}}' \
      | grep "|localhost:5001/${svc}:" \
      | grep -v ":latest$" \
      | grep -v ":${IMAGE_TAG}$" \
      | sort -r \
      | tail -n +2 \
      | cut -d'|' -f2 \
      | xargs -r docker rmi 2>/dev/null || true
  done
  docker image prune -f >/dev/null 2>&1 || true
fi
```

### 9b. 清 Registry 端(本地清完之后执行)

策略:**和本地对齐**。本地现在剩下的 tag 集合就是"应保留",Registry 里比这个集合多出来的 tag 全删,再跑 GC 回收 blob。

Registry 必须启动时带 `REGISTRY_STORAGE_DELETE_ENABLED=true`;当前 `my-registry` 容器已开启,无此变量的环境第一次 DELETE 会返回 405,脚本自动跳过 GC 并提示。

```bash
if [ "${KEEP_HISTORY}" != "1" ]; then
  REG_DELETE_OK=1
  for svc in ${TARGETS}; do
    LOCAL_TAGS=$(docker images "localhost:5001/${svc}" --format '{{.Tag}}' \
      | grep -v '^<none>$' | sort -u)
    REG_TAGS=$(curl -sf "http://localhost:5001/v2/${svc}/tags/list" \
      | python3 -c 'import json,sys; d=json.load(sys.stdin); print("\n".join(d.get("tags") or []))' \
      | sort -u)
    TO_DELETE=$(comm -23 <(echo "$REG_TAGS") <(echo "$LOCAL_TAGS"))
    OK=0; FAIL=0
    # 必须用 while-read,不能用 `for tag in $TO_DELETE` —— 后者会吃掉部分循环
    while IFS= read -r tag; do
      [ -z "$tag" ] && continue
      DIGEST=$(curl -sI \
        -H 'Accept: application/vnd.oci.image.index.v1+json' \
        -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' \
        -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' \
        "http://localhost:5001/v2/${svc}/manifests/${tag}" \
        | awk 'tolower($1)=="docker-content-digest:"{print $2}' | tr -d '\r')
      [ -z "$DIGEST" ] && { FAIL=$((FAIL+1)); continue; }
      HTTP=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
        "http://localhost:5001/v2/${svc}/manifests/${DIGEST}")
      case "$HTTP" in
        202) OK=$((OK+1)) ;;
        405) FAIL=$((FAIL+1)); REG_DELETE_OK=0 ;;
        *)   FAIL=$((FAIL+1)); echo "  registry DELETE ${svc}:${tag} HTTP ${HTTP}" ;;
      esac
    done <<< "$TO_DELETE"
    echo "  registry ${svc}: deleted ${OK}, failed ${FAIL}"
  done
  if [ "$REG_DELETE_OK" = "1" ]; then
    # 禁用 -m!registry:2 的 garbage-collect -m 对 multi-arch manifest list 有 bug,
    # 会把被 index 引用的子 manifest 和 layer blob 也当 dangling 清掉,导致 pull 失败。
    # 无 flag 模式只清真正的 unreferenced blob,安全。
    docker exec my-registry bin/registry garbage-collect /etc/docker/registry/config.yml >/dev/null 2>&1 \
      || echo "  registry GC failed (非致命)"
    echo "Registry 占用:$(docker exec my-registry du -sh /var/lib/registry 2>/dev/null | awk '{print $1}')"
  else
    echo "Registry GC 跳过:请重启 my-registry 时加 -e REGISTRY_STORAGE_DELETE_ENABLED=true"
  fi
fi
```

**注意**:
- `localhost:5001/${svc}:latest` 永远保留(步骤 6 会把 latest 指向本次 IMAGE_TAG)
- 上一次 IMAGE_TAG 保留,回滚只需 `SERVER_TAG=<上次的 tag> docker compose up -d synapse`(下文"常用命令"里有)
- Registry DELETE 只删 manifest,实际层回收由 GC 完成;GC 会短暂只读,Registry 自己处理并发较脆弱,**并发部署**时应串行跑
- 删除的是 manifest list digest,子 manifest 和 config/layer blob 由 GC 按引用计数回收
- 传了 `keep` 参数时跳过整块,保留全部历史 tag(大范围回滚、版本比对场景)

## 步骤 10：输出结果

部署成功后输出：
- 部署的服务和镜像版本
- 各服务运行状态（`docker compose ps`）
- 后端地址（如部署了）：`http://localhost:8080`
- 前端地址（如部署了）：`http://localhost:3080`
- 仓库中的历史版本
- 常用命令提示：
  - 查看日志：`docker compose logs -f`
  - 停止服务：`docker compose down`
  - 查看仓库所有镜像：`curl -s http://localhost:5001/v2/_catalog`
- 回滚说明:默认保留上一次 IMAGE_TAG,回滚到上一版:
  - 后端：`SERVER_TAG=<上一次 tag> docker compose up -d synapse`
  - 前端：`WEB_TAG=<上一次 tag> docker compose up -d synapse-web`
  - 查看本地剩余 tag：`docker images 'localhost:5001/synapse*'`
  - 需要更长回滚窗口时用 `/deploy keep` 保留所有历史
