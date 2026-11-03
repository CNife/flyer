# flyer 开发任务入口。pre-commit 与 CI 调用的都是这里的 recipe。
set shell := ["bash", "-euo", "pipefail", "-c"]

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X github.com/CNife/flyer/internal/cli.Version=" + version

# 列出所有任务
default:
    @just --list

# 一次性：把 .githooks 挂到 git
setup:
    git config core.hooksPath .githooks
    chmod +x .githooks/pre-commit
    @echo "pre-commit 已启用；需要跳过时用 git commit --no-verify"

# 构建 dist/flyer
build:
    mkdir -p dist
    CGO_ENABLED=0 go build -trimpath -ldflags "{{ ldflags }}" -o dist/flyer ./cmd/flyer

# 就地格式化
fmt:
    gofmt -l -w .

# 校验格式，不改文件
fmt-check:
    @out="$(gofmt -l .)"; if [ -n "$out" ]; then echo "$out"; echo "格式不符合 gofmt，运行 just fmt" >&2; exit 1; fi

# 校验 go.mod/go.sum 已 tidy
tidy-check:
    go mod tidy -diff

# go vet
vet:
    go vet ./...

# 单元测试（随机顺序）
test:
    go test -shuffle=on ./...

# 竞态检测，CI 的 Linux job 用
test-race:
    go test -race -shuffle=on ./...

# 需要本机装 golangci-lint，版本与 .github/workflows/ci.yml 保持一致
lint:
    golangci-lint run

# 漏洞扫描（版本锁在 go.mod 的 tool 指令里）
vuln:
    go tool govulncheck ./...

# 校验 workflow 语法
workflows:
    go tool actionlint

# 提交前快检
check: fmt-check tidy-check vet test

# 本地全量门禁
ci: check lint vuln workflows
