# 企业级知识库 Roadmap

> 目标:per-org 多源知识库(code / docs / bugs / images / DBs),供 agent 网络自主编排查询与写入。本文档按「离 agent 可用还差什么」排优先级,每项用稳定 ID 便于后续单点讨论与追踪。

## 当前基线(2026-04)

- **摄取**:HTTP multipart 上传,Markdown 单一格式,content-hash 去重,`source` 字段区分 `user` / `ai-generated`
- **切分**:递归分隔符(`\n\n` → `\n` → `. ` → ` ` → 字符窗口),默认 1500 runes / 150 overlap
- **向量化**:Azure text-embedding-3-large(1536 维)+ fake provider,单向量/chunk
- **存储**:MySQL(文档元数据)+ PG/pgvector(chunks,HNSW cosine)
- **检索**:单路向量(`SearchChunks`)+ 文档级语义搜索 + 标题 LIKE 模糊搜
- **生命周期**:硬删、原地 overwrite(原子 swap)、async reindex、pending/failed 状态
- **权限**:仅 org 隔离
- **观测**:`index_status` 字段 + 日志,无 metrics/dashboard
- **评测**:`cmd/evalretrieval` 已落地 hit@k / MAP / MRR

---

## Tier 1 — 检索质量(Agent 可用性底线)

### T1.1 混合检索(BM25 + 向量 + RRF)
**问题**:纯向量丢失字面信号(错误码、stack trace、路径、ID、人名)。
**范围**:PG `tsvector` + GIN 索引加到 `document_chunks`;查询层并行跑两路,用 Reciprocal Rank Fusion 合并。
**关键点**:中文需要分词(jieba / ICU),默认 `to_tsvector('simple', ...)` 对中文无效。

### T1.2 Reranker 二阶段
**问题**:向量 top-20 的精确度 40-60%,塞给 LLM 浪费 context。
**范围**:召回扩到 50 → cross-encoder 重排 → 截到 5-10 条返回。
**选型**:BGE-reranker(自建)/ Cohere Rerank(托管)/ Voyage。关注 P99 延迟 < 300ms。

### T1.3 结构感知切分 + parent-child
**问题**:递归分隔符对 markdown heading、代码函数、表格一视同仁,语义边界被切断。
**范围**:
- Markdown:按 heading 层级(h1→h2→h3)切,保留祖先路径作为 metadata
- 代码:tree-sitter 按函数/类切
- 表格:整块保留不切
- **parent-child**:召回粒度用子 chunk(精度),返回粒度用父 chunk(上下文)
**关键点**:单点收益最大的一项,影响所有下游检索质量。

### T1.4 结构化 metadata 过滤
**问题**:只能按 `org_id + index_status` 过滤。Agent 需要「path prefix + date range + doc_type + tag」类组合条件。
**范围**:`document_chunks` 加 `metadata jsonb` 列 + GIN 索引;检索 API 支持任意 JSON path 过滤 + 向量联合查询(先过滤后 ANN 或 post-filter)。

---

## Tier 2 — 源多样性(对齐 memory 里多源目标)

### T2.1 代码仓库接入
**问题**:目前无法索引 code。memory 明确要求。
**范围**:Git pull(或 webhook)+ tree-sitter AST 切分(函数/类)+ 符号表(定义/引用/跨文件边)入独立结构。
**关键点**:符号图是「agent 跳转到引用」的前置,纯文本 chunk 不够用。

### T2.2 Bug / ticket 接入
**问题**:缺 bug 追溯能力。
**范围**:Linear / Jira / GitHub Issues webhook,结构化字段(状态/优先级/责任人/关联 PR)进 metadata,正文进 chunks。

### T2.3 图片接入
**问题**:产品截图、架构图无法检索。
**范围**:VLM 生成 caption → 作为 text chunk 入库(主路径,可读 + 可 grep);可选 CLIP 跨模态向量(补路径)。

### T2.4 DB schema + sample 接入
**问题**:agent 无法「知道 org 有哪些表」,阻塞自主 SQL 能力。
**范围**:对表结构 + 样例行生成语义描述,入 chunks。定期 drift 检测。

### T2.5 增量同步
**问题**:manual upload ≠ enterprise。内容几周就过时。
**范围**:
- Git webhook → 仅重跑改动文件
- 定时 drift scan(`last_seen` 落后阈值的源标待同步)
- 幂等:content_hash 没变就不重跑 embedding

---

## Tier 3 — Agent 特有需求

### T3.1 Agent 可写 CRUD API
**问题**:memory 说「agents can directly CRUD」,当前只有读路径。
**范围**:
- `CreateDocument` / `UpdateDocument` / `AppendSection` / `DeleteDocument` 编程 API(非 HTTP multipart)
- 结构化查询(非语义):`list docs where tag=X and author=Y`
- 事务性批量写(一次产出多篇关联文档)

### T3.2 Provenance / 引用
**问题**:Agent 回答必须可审计,追到具体 chunk。
**范围**:检索返回带 `(doc_id, chunk_idx, file_path, heading_path, line_range)` 锚点;LLM prompt 模板带 anchor;输出解析回填引用。

### T3.3 Query 改写 / HyDE / 多查询
**问题**:Agent 生成的 query 欠佳(过短、过泛)。
**范围**:
- **HyDE**:小模型生成假设答案 → embed 它检索
- **Multi-query**:一个 query 拆 3-5 个子 query,并发检索后 RRF
- **路由**:小模型判定 query 走向量 / 关键词 / 混合

---

## Tier 4 — Enterprise 合规

### T4.1 Doc 级 ACL
**问题**:org 内 HR / 财务 / IP 文档需区分可见范围。**Schema 改动,越晚做越贵**。
**范围**:
- `documents.acl_group_id`(多对多或 bitmap)
- 检索必带 `user_id`,PG 查询 JOIN ACL 过滤
- **关键点**:Agent 代表 user 查时,用 user 的 ACL,不是 agent 自己的 —— agent KB 独有坑

### T4.2 版本 / 审计
**问题**:当前 overwrite 原地替换,丢历史。SOC2 / ISO27001 要求「date X 时 KB 里是什么」。
**范围**:
- 软删除 + `documents.version`,overwrite 生成新版本保留旧行
- `access_log` 表:who / when / query / returned chunks

### T4.3 PII / 敏感信息扫描
**问题**:法务要求。
**范围**:入库前 regex(SSN / API key / email / 卡号)+ 可选 ML 分类;命中打标,可选自动脱敏。

---

## Tier 5 — 运营化

### T5.1 端到端 eval(RAGAS 维度)
**问题**:当前 hit@k / MAP 只覆盖检索层,缺生成端指标。
**范围**:
- **Faithfulness**:给定 chunks,LLM 有无幻觉
- **Answer relevance**:回答是否真的回答了 query
- **Citation accuracy**:引用对不对
**选型**:RAGAS / TruLens 模式。

### T5.2 使用率 observability
**问题**:不知道哪些 doc 是死文档、检索是否漂移、embedding 成本多少。
**范围**:
- 每 chunk 的查询命中次数 → 找死文档归档
- Query → 相似度分布热图 → 检测漂移
- 每次检索 latency / cost / embedding token 消耗

### T5.3 批量摄取管线
**问题**:单篇 upload 跑不动 10w+ 文档的企业语料。
**范围**:并发 chunker + embedding + rate limit + backpressure;断点续传(记 cursor);失败重试队列。

### T5.4 Graph 关联层(可选,高价值)
**问题**:code / docs / bugs 之间的关系向量搜不出来。
**范围**:轻量实体抽取 + `links` 表,支持「架构文档 → 相关 ticket → 相关 PR → 相关代码文件」跨类型跳转。

---

## 推进建议

**P0(阻塞 agent 落地)**:T1.1、T1.2、T1.3、T1.4 全部 —— 不做这几项,agent 拿到的 context 噪声太大,提示工程救不回来。

**P1(对齐多源目标)**:T2.1(代码接入)+ T3.1(Agent CRUD)+ T3.2(provenance)—— 「agent 网络」最小闭环。

**P2(企业客户 day 0 需求)**:T4.1(ACL)—— schema 级改动,越晚越贵。

**P3**:剩余 T2 多源 + T5 运营化。

**三个最容易被低估但回报最大的**:T1.3(结构感知切分)、T1.2(rerank)、T4.1(ACL)。前两个对检索质量是乘法级提升,第三个是 schema 成本门槛。
