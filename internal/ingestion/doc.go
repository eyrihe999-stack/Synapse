// Package ingestion 是 Synapse 数据摄取的统一 pipeline。
//
// 设计目标:把"从外部数据源拉内容 → 归一化 → 切分 → 向量化 → 落库"这条链路
// 从各模块(code / document / 未来 bug / image / db)里收敛到一个地方。新增一种
// source type 不需要改 pipeline 本身,只实现三件:
//
//   - Fetcher 子包(ingestion/source/*)     :从源端拉数据,输出 NormalizedDoc 流
//   - Chunker 子包(ingestion/chunker/*)    :按 source type 的结构提示切分成 IngestedChunk
//   - Persister 子包(ingestion/persister/*):把 chunks + 向量落到对应的类型化表
//
// 各 source type 的存储 schema / DAO 继续住在 internal/code、internal/document 里 ——
// persister 是薄适配器,import 现有 repository,不把 DAO 搬进 ingestion。
//
// 顶层(本包根目录)只放公用骨架:
//
//   - NormalizedDoc  :统一中间表示(闸口)
//   - IngestedChunk  :embed 前的切片形态
//   - Fetcher/Chunker/Persister :三个接口
//   - Pipeline       :编排器
//   - Registry       :按 source_type 路由 chunker / persister
//
// 并发:Pipeline 常驻,Run 不共享可变状态。Fetcher 按每次 ingest 构造(携带 user token
// / 本次上传 body 等一次性上下文),用完丢弃。
package ingestion
