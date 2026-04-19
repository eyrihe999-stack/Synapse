.PHONY: run build test clean goldseed eval eval-csv

run:
	APP_ENV=dev go run ./cmd/synapse/

build:
	go build -o bin/synapse ./cmd/synapse/

test:
	go test ./...

clean:
	rm -rf bin/

# ─── 检索评估 ───────────────────────────────────────────────────────────────
# 用法:
#   make goldseed ORG=123                  # 生成半填好的 gold 模板到 testdata/retrieval_gold.yaml
#   make eval ORG=123                      # 跑一次评估
#   make eval-csv ORG=123                  # 跑评估并追加一行汇总到 runs/eval.csv

# 强制要求 ORG 变量:缺失时直接报错,避免跑到默认值误解结果。
require-org:
	@if [ -z "$(ORG)" ]; then \
		echo "ERROR: ORG is required, e.g. make $(MAKECMDGOALS) ORG=123"; \
		exit 1; \
	fi

goldseed: require-org
	APP_ENV=dev go run ./cmd/goldseed --org $(ORG) > testdata/retrieval_gold.yaml
	@echo "→ 已生成 testdata/retrieval_gold.yaml,现在填写每条的 query 再跑 make eval。"

eval: require-org
	APP_ENV=dev go run ./cmd/evalretrieval --org $(ORG)

eval-csv: require-org
	APP_ENV=dev go run ./cmd/evalretrieval --org $(ORG) --csv runs/eval.csv
