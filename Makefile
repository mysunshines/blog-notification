# ============================================================
# notification-service 构建脚本
# ============================================================
# 常用目标：
#   make build    编译到 bin/（注入 git 版本号）
#   make test     跑单元测试（WS hub 并发用例建议加 -race：go test -race ./...）
#   make proto    从 proto/notification.proto 重新生成 pb（需本地安装 protoc）
#   make docker   构建容器镜像（版本号取 git describe）
# ============================================================

.PHONY: all build run test clean deps update proto docker docker-run lint fmt help

SERVICE_NAME=notification-service
BINARY_NAME=$(SERVICE_NAME)
SRC_DIR=cmd/server
BIN_DIR=bin
# 宿主机端口映射与 docker-compose.yml 保持一致（9105=gRPC，8085=WS/探活，9099=Metrics）
PORTS=8085:8085 9105:9105 9099:9099

GIT_VERSION      := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
VERSION_LDFLAGS := -X main.Version=$(GIT_VERSION)

MODULE := github.com/mysunshines/blog-notification
PROTO_DIR := proto
PROTO_OUT := $(PROTO_DIR)/pb
PROTOC_OPTS := --go_out=. --go_opt=module=$(MODULE) --go-grpc_out=. --go-grpc_opt=module=$(MODULE)

all: build

deps:
	go mod tidy
	go mod download

update:
	go get -u ./...

# 重新生成 gRPC 代码：pb 产物提交入库，无 protoc 环境的机器无需重新生成
proto:
	@if [ -d proto ]; then \
		mkdir -p proto/pb && \
		cd proto && protoc $(PROTOC_OPTS) *.proto; \
	else \
		echo "==> No proto directory"; \
	fi

build:
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) ./$(SRC_DIR)

run:
	go run ./$(SRC_DIR)

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

docker:
	docker build -t $(SERVICE_NAME):$(GIT_VERSION) --build-arg GIT_VERSION=$(GIT_VERSION) .

docker-run: docker
	docker run --rm -p $(PORTS) --name $(SERVICE_NAME) $(SERVICE_NAME):$(GIT_VERSION)

lint:
	go vet ./...

fmt:
	go fmt ./...

help:
	@echo "Targets: build run test clean deps update proto docker docker-run lint fmt"
