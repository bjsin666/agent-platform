# Go 后端多阶段构建:阶段1 编译,阶段2 精简运行镜像
# 构建: docker build -t agent-backend .
FROM golang:1.25-alpine AS builder
WORKDIR /src
# 国内拉取依赖走 goproxy.cn 镜像(避免直连 proxy.golang.org 超时)
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
# 先只拷贝依赖描述文件,利用 Docker 层缓存:go.mod 没变就不重复下载依赖
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY config.yaml ./
# CGO_ENABLED=0 产出纯静态二进制,可在 alpine 里跑
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
# 时区与 CA 证书(调用 HTTPS API 需要)
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/server /app/server
# 非敏感配置;敏感项走 compose 的 env_file(.env),不进入镜像
COPY config.yaml /app/config.yaml
# 演示页
COPY web/ /app/web/
EXPOSE 8080
CMD ["/app/server"]