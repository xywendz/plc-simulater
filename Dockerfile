# ---- 前端构建阶段（Web 模板：--mode web -> dist-web）----
FROM node:22-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build:web

# ---- Go 构建阶段（Web 入口，纯 Go）----
FROM golang:1.27-alpine AS builder
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /app/frontend/dist-web/ ./internal/webui/dist/
RUN --mount=type=cache,target=/go/pkg/mod CGO_ENABLED=0 GOOS=linux \
    go build -o /app/server ./cmd/plc-simulater-server

# ---- 运行阶段 ----
FROM alpine:3.20
WORKDIR /app
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && echo "Asia/Shanghai" > /etc/timezone
ENV TZ=Asia/Shanghai
COPY --from=builder /app/server /app/server
COPY --from=builder /app/config.yaml /app/config.yaml
VOLUME /app
EXPOSE 102 8070
ENTRYPOINT ["/app/server","-config","config.yaml"]
