# 使用官方的 Go 镜像作为构建环境
FROM golang:1.25-alpine AS builder

WORKDIR /app

# 复制 go.mod 和 go.sum 文件
# 这一步可以被缓存，如果依赖没有变化，后续构建会更快
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制所有源代码
COPY *.go ./

# 编译应用，禁用 CGO 以创建静态链接的二进制文件
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o rss-server .

# ---

# 使用一个非常小的基础镜像来运行程序
FROM alpine:latest  

# 安装 ca-certificates 以支持 HTTPS 请求
RUN apk --no-cache add ca-certificates tzdata

# 设置时区
ENV TZ=Asia/Shanghai

WORKDIR /app/

# 从 builder 阶段复制编译好的二进制文件
COPY --from=builder /app/rss-server .

# 暴露端口
EXPOSE 8888

# 运行应用
CMD ["./rss-server"]