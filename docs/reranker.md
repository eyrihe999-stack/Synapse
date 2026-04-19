# Reranker 运维 Notes(T1.2)

## 架构

```
SearchChunks(query, topK=10, opts.Rerank=true)
  │
  ├─→ retrieveChunks(topK × 5 = 50 候选)      ← retrieve 阶段(vector / BM25 / hybrid)
  │
  ├─→ reranker.Rerank(query, 50 个 chunk.Content)
  │      ↓
  │   HTTP POST → TEI 服务(BGE-reranker-v2-m3)
  │      ↓
  │   返回 50 个 (index, score) 按 score 降序
  │
  └─→ 按 rerank 顺序截取 top 10 → DTO
```

`Reranker` 接口定义见 `pkg/reranker/reranker.go`,BGE-TEI 实现见 `bge_tei.go`。

## 启用

### 1. 起 TEI 服务

**x86 Linux 主机(推荐生产):**
```bash
docker run -d --name synapse-tei \
  -p 8082:80 \
  ghcr.io/huggingface/text-embeddings-inference:cpu-latest \
  --model-id BAAI/bge-reranker-v2-m3
```

**GPU 主机(更快):**
```bash
# Turing (T4 / RTX 20-series)
docker run -d --name synapse-tei \
  --gpus all -p 8082:80 \
  ghcr.io/huggingface/text-embeddings-inference:turing-1.5 \
  --model-id BAAI/bge-reranker-v2-m3
```

**Apple Silicon(M 系列)开发机:**

TEI 官方镜像 `cpu-latest` 只有 `linux/amd64`,需要 Rosetta 模拟:
```bash
docker run -d --name synapse-tei \
  --platform linux/amd64 \
  -p 8082:80 \
  ghcr.io/huggingface/text-embeddings-inference:cpu-latest \
  --model-id BAAI/bge-reranker-v2-m3
```
仿真模式 ~2-3x 慢,**只适合开发测试,不要上生产**。

### 2. 首次启动会下载模型

TEI 首启时从 HuggingFace 拉 `BAAI/bge-reranker-v2-m3`(~560MB 权重)。
中国大陆网络从 HF 下载可能慢,预估 5-15 分钟。
下载完毕会打印 `Ready` 日志,端口开始接收请求。

可以挂 volume 缓存模型:
```bash
docker run -d --name synapse-tei \
  -p 8082:80 \
  -v synapse-tei-models:/data \
  ghcr.io/huggingface/text-embeddings-inference:cpu-latest \
  --model-id BAAI/bge-reranker-v2-m3
```

### 3. 打开 synapse 配置

`config/config.dev.yaml`:
```yaml
reranker:
  provider: "bge_tei"
  bge_tei:
    base_url: "http://127.0.0.1:8082"
    timeout: "5s"
```

### 4. 调用侧开启

HTTP / 内部 agent 调用 `SearchChunks` 时传:
```go
&docsvc.SearchChunksOptions{Rerank: true}
```

eval 批跑:
```bash
go run ./cmd/evalretrieval --org 1 --rerank --rerank-url http://127.0.0.1:8082 --tag phase1.2-rerank
```

## Smoke 测试

TEI 起来后直接 curl 验:
```bash
curl -X POST http://127.0.0.1:8082/rerank \
  -H 'Content-Type: application/json' \
  -d '{"query":"stripe webhook","texts":["stripe integration","auth module","unrelated"],"raw_scores":true}'
```

应返三条 JSON,按 score 降序,index 指回输入 texts 位置。

## 性能预期

| 环境 | 50 pairs 延迟 | 备注 |
|---|---|---|
| x86 CPU(16 核服务器) | ~200-400ms | 够用 |
| T4 GPU | ~30-60ms | 推荐产线 |
| M1 Mac + Rosetta | ~1-3s | 仅开发 |

topK=10, candidate=50,rerank 延迟基本是 retrieve 延迟的 1-3 倍 —— 用户感知上 search 总延迟从 ~300ms 变成 ~600-1000ms。

## 降级行为

- `config.reranker.provider=""`(默认):不启用,`opts.Rerank=true` 时 warn 后 no-op,走 retrieve 原顺序
- TEI 服务挂了:单次请求失败,warn 日志,本次返回 retrieve 原顺序 top-K(不影响其他请求)
- 连续失败:由下游监控告警(metrics 字段 TODO:加 `reranker_error_total` 计数器)

**关键保证:rerank 是精度优化,不是功能必需 —— 失败永不导致搜索整体不可用。**

## 调优

- **topK × 5 的倍率**:写死在 `search_service.go:rerankCandidateMultiplier`。3 ~ 10 都合理,太小 rerank 发挥不开,太大延迟线性爆炸。
- **模型选择**:v2-m3 是 SOTA 多语言;如果只有英文语料可换 `bge-reranker-base-en-v1.5`(~270MB,更快)。
- **超时**:默认 5s 偏保守。产线稳定后可降到 2s,加 p99 监控。
