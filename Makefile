.PHONY: help build run test test-pg test-track vet tidy fmt fmt-check check clean dev-start dev-stop probe

# 默认目标：打印可用命令（比直接跑 build 更友好）。
.DEFAULT_GOAL := help

help:
	@echo "nongyu-backend-plus —— 常用命令"
	@echo ""
	@echo "  make build       构建到 bin/server"
	@echo "  make run         本地启动（默认 :8080；读环境变量，不自动读 .env）"
	@echo "  make dev-start   用 scripts/dev-start.sh 起服务（读 .env + 探测端点）"
	@echo "  make dev-stop    停掉 dev-start 起的服务"
	@echo "  make probe       探测正在运行的服务端点"
	@echo ""
	@echo "  make test        跑全部测试（未配 DATABASE_URL / MINIO_ENDPOINT 时相关用例自动 SKIP）"
	@echo "  make test-pg     只跑 PostgreSQL 集成测试（需要 DATABASE_URL）"
	@echo "  make test-track  只跑新增四域的测试（内容/消息/埋点/装配）"
	@echo ""
	@echo "  make vet         静态检查"
	@echo "  make fmt         格式化"
	@echo "  make fmt-check   检查格式（CI 用，不改文件）"
	@echo "  make check       vet + fmt-check + test，提交前跑这个"

# 构建到 bin/server
build:
	go build -o bin/server ./cmd/server

# 本地启动（默认 :8080）。
#
# 注意：这里**不自动加载 .env**——Go 程序不会读 .env 文件，
# 环境变量要由 shell 提供。想「读 .env 再启动」请用 make dev-start。
run:
	go run ./cmd/server

# 用脚本启动：读 .env、等就绪、打印装配摘要、逐端点探测。
dev-start:
	@bash scripts/dev-start.sh

dev-stop:
	@bash scripts/dev-start.sh --stop

probe:
	@bash scripts/dev-start.sh --probe-only

# 单元测试。
#
# 未配 DATABASE_URL / MINIO_ENDPOINT 时，PG 与 MinIO 的集成用例会自动 SKIP，
# 所以这个目标在任何环境都能跑（这正是当初把集成测试写成「无处可连则 skip」的原因）。
test:
	go test ./...

# 只跑 PostgreSQL 集成测试。
#
# 需要真实库；用例按 schema 隔离，可与其它包的 PG 测试并行。
test-pg:
	@test -n "$$DATABASE_URL" || (echo "需要设置 DATABASE_URL"; exit 1)
	go test ./internal/platform/pgstore/ -run PG -count=1 -v

# 只跑本 effort 新增四域的测试（改这几块时的快速反馈）。
test-track:
	go test ./internal/content/... ./internal/message/... ./internal/track/... ./internal/app/... -count=1

# 静态检查
vet:
	go vet ./...

# 整理依赖
tidy:
	go mod tidy

# 格式化
fmt:
	gofmt -w .

# 检查格式但不改文件（CI/提交前用）。
#
# 注意行尾：仓库已加 .gitattributes 强制 LF。若在 Windows 上看到大量
# 「格式不合规」而 diff 全是整行红绿，是工作区被检出成 CRLF 导致的，
# 用 `git add --renormalize .` 或重新 clone 即可，不是真的格式问题。
fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "以下文件格式不合规，请跑 make fmt："; \
		echo "$$out"; \
		exit 1; \
	fi

# 提交前跑这一条：静态检查 + 格式 + 全部测试。
#
# 顺序是有意的——vet 和 fmt-check 便宜且失败快，放在 test 前面，
# 让人先看到「代码写法有问题」而不是在测试输出里翻半天。
check: vet fmt-check test

# 清理产物
clean:
	rm -rf bin/ dist/
