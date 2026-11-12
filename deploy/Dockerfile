# 多阶段构建：编译阶段用完整工具链，运行阶段只带二进制。
#
# 目标镜像 ≈ 15MB（静态编译 + distroless/static），因为：
#   - 本仓无 CGO 依赖（已确认无 import "C"），可 CGO_ENABLED=0 静态编译；
#   - 服务是「无状态」的（读写全在 Postgres / Redis / MinIO），
#     镜像里不需要任何数据目录或配置文件。
#
# 注意 go 版本要与 go.mod 的 `go 1.26.0` 对齐；升级 Go 时同步改这里。

# ---------- 编译阶段 ----------
FROM golang:1.26 AS build

WORKDIR /src

# 先只拷依赖清单，让 go mod download 这一层能被缓存——
# 否则每次改一行代码都要重新下载全部依赖。
COPY go.mod go.sum ./
RUN go mod download

# 再拷源码。
COPY . .

# CGO_ENABLED=0 是静态编译的前提；-trimpath 去掉本机路径（避免把开发机目录
# 打进二进制）；-ldflags="-s -w" 去掉符号表与调试信息（镜像更小）。
# 注意：这会让堆栈里的文件名仍是相对路径，排查时依然可用。
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/server ./cmd/server

# ---------- 运行阶段 ----------
# distroless/static 只有证书与 zoneinfo，没有 shell、没有包管理器。
# 用非 root 用户运行（distroless 的 nonroot 是 uid 65532）。
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# TLS 根证书：出站访问学校系统（https）必须有。
# distroless/static 自带 ca-certificates，这里不需要额外拷。

COPY --from=build /out/server /app/server

# 服务默认监听 :8080（ADDR 可覆盖）。EXPOSE 只是文档性质。
EXPOSE 8080

USER nonroot:nonroot

# 用 /healthz 做容器级健康检查（只报进程存活，不探下游——
# 探下游会让「数据库短暂抖动」导致容器被重启，反而放大故障）。
# distroless 没有 curl，用二进制自带的能力：这里用 Go 的 net/http 不行，
# 所以改用无参调用 + 依赖编排层的探活。若需 HTTP 探活，请用 K8s 的
# httpGet probe（见 deploy/k8s-note.md），而不是在镜像里塞 curl。
ENTRYPOINT ["/app/server"]
