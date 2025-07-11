# 构建阶段
FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o l2tp-manager .

# 运行阶段
FROM alpine
RUN apk add --no-cache ca-certificates curl
WORKDIR /app
COPY --from=builder /build/l2tp-manager .

# 环境变量
ENV PORT=8080
ENV DATABASE_PATH=/app/data/l2tp_manager.db
ENV PRODUCTION=true

# 创建数据目录
RUN mkdir -p /app/data

EXPOSE 8080
VOLUME ["/app/data"]
CMD ["./l2tp-manager"] 