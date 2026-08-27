SHELL := /bin/bash
BINARY := sail

.DEFAULT_GOAL := help

help: ## 显示可用目标
	@awk -F'## ' '/^[a-zA-Z][a-zA-Z0-9_-]*:.*## /{t=$$1; sub(/:.*/, "", t); printf "  \033[36m%-13s\033[0m %s\n", t, $$2}' $(MAKEFILE_LIST)

build: ## 编译当前平台二进制到 ./$(BINARY)
	go build -ldflags "-s -w" -o $(BINARY) .

tidy: ## go mod tidy
	go mod tidy

vet: ## go vet
	go vet ./...

test: vet ## 静态检查 + 单测
	go test ./...

e2e: build ## 端到端验证(需凭证:复用 ~/.sail/config.yaml 或 SAIL_E2E_* 变量)
	./scripts/e2e.sh

release: ## 编译并发布到 npm(未登录会引导 npm login): make release VERSION=0.1.0
	./scripts/release.sh $(VERSION)

release-dry: ## 发布预演(不真发,无需登录): make release-dry VERSION=0.1.0
	./scripts/release.sh $(VERSION) --dry-run

clean: ## 清理本地构建产物
	rm -f $(BINARY)
	rm -rf npm/darwin-*/bin npm/linux-*/bin
	rm -rf dist
