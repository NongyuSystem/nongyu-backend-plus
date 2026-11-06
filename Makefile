.PHONY: build run test vet tidy fmt clean

# 构建到 bin/server
build:
	go build -o bin/server ./cmd/server

# 本地启动（默认 :8080）
run:
	go run ./cmd/server

# 单元测试
test:
	go test ./...

# 静态检查
vet:
	go vet ./...

# 整理依赖
tidy:
	go mod tidy

# 格式化
fmt:
	gofmt -w .

# 清理产物
clean:
	rm -rf bin/ dist/
