FROM golang:1.25-alpine AS builder
WORKDIR /app

# 复制模块文件
COPY go.mod go.sum ./
RUN go mod download

# 复制全部源代码
COPY . .

# 构建静态二进制
RUN CGO_ENABLED=0 GOOS=linux go build -o web2rss ./.

# 运行阶段
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=builder /app/web2rss .

EXPOSE 8888

CMD ["./web2rss"]