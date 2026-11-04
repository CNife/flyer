# flyer

把本地 HTML 发布成可分享的网页。默认 1 小时过期，也可以永久；支持密码保护、二维码和命令行管理。

服务端是单个 Go 二进制，页面存 SQLite + 本地文件。公网入口由 Caddy 终止 TLS 并反代到 `127.0.0.1:8787`。

## 安装

npm（推荐给 agent；首次运行会下载对应平台的二进制并校验 `checksums.txt`）：

```bash
npx -y @cnife/flyer@latest publish report.html
npm install -g @cnife/flyer
```

Go：

```bash
go install github.com/CNife/flyer/cmd/flyer@latest
```

也可以直接下载 [GitHub Releases](https://github.com/CNife/flyer/releases) 的二进制或压缩包。当前不提供 Homebrew 和 AUR 包。

## 构建

```bash
go build -o dist/flyer ./cmd/flyer
# 或
just build
```

## 开发

需要 Go 1.27+ 与 `just`；跑 lint 还需要 `golangci-lint`（版本与 CI 一致）。Arch：

```bash
sudo pacman -S just golangci-lint
```

```bash
just setup   # 挂载 .githooks/pre-commit，每个 clone 一次
just check   # 提交前快检：gofmt / go mod tidy / go vet / go test
just ci      # 全量门禁：check + golangci-lint + govulncheck + actionlint
just --list  # 全部任务
```

推送前走 no-mistakes 门禁（每个 clone 先 `no-mistakes init`）：

```bash
git push no-mistakes <branch>   # review / test / lint 通过后自动推送并开 PR
no-mistakes axi                 # 查看当前分支的 run 状态
```

门禁命令和审查策略在 `.no-mistakes.yaml`，与 `just` 的 recipe 一致。

## 服务端部署

```bash
# 系统用户和目录
sudo useradd --system --home /opt/flyer --shell /usr/sbin/nologin flyer
sudo install -d -o flyer -g flyer -m 0750 /opt/flyer

# 二进制：生产部署使用 GitHub Release 资产，不要用本地 dist/flyer
VERSION=0.2.0   # 替换为当前发布版本
curl -fLO "https://github.com/CNife/flyer/releases/download/v${VERSION}/flyer_${VERSION}_linux_amd64"
curl -fLO "https://github.com/CNife/flyer/releases/download/v${VERSION}/checksums.txt"
grep " flyer_${VERSION}_linux_amd64$" checksums.txt | sha256sum -c -
sudo install -o flyer -g flyer -m 0750 "flyer_${VERSION}_linux_amd64" /opt/flyer/flyer

# 生成配置，stdout 会打印 admin key（只显示一次）
sudo -u flyer /opt/flyer/flyer init-server \
  --dir /opt/flyer \
  --base-url https://flyer.cnife.cn

# systemd
sudo install -m 0644 deploy/flyer.service /etc/systemd/system/flyer.service
sudo systemctl daemon-reload
sudo systemctl enable --now flyer
```

Caddy 站点配置：

```caddyfile
flyer.cnife.cn {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8787
}
```

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
curl https://flyer.cnife.cn/health
```

### Docker

镜像同时提供 `linux/amd64` 和 `linux/arm64`：

```bash
docker run -d --name flyer \
  -p 127.0.0.1:8787:8787 \
  -v flyer-data:/data \
  -e FLYER_BASE_URL=https://flyer.example.com \
  ghcr.io/cnife/flyer:latest

docker logs flyer 2>&1 | head    # 首次启动会打印 admin key
```

或者用仓库里的 `compose.yaml`：

```bash
FLYER_BASE_URL=https://flyer.example.com docker compose up -d
```

容器内监听 `0.0.0.0:8787`，默认只绑宿主机的 `127.0.0.1:8787`；前面仍要放 Caddy/Traefik 做 TLS。`/data` 持久化 `config.toml`、SQLite 和页面文件。首次启动会按 `FLYER_BASE_URL` 生成配置；之后修改域名，请编辑卷里的 `config.toml` 并重启。若用 bind mount 而不是 named volume，宿主机目录需要属于 `10001:10001`。

### 服务端目录

```text
/opt/flyer/
├── flyer          # 二进制
├── config.toml    # 配置（含 admin key 和 cookie secret）
├── flyer.db       # SQLite
└── pages/         # HTML 文件
```

`config.toml` 默认：

```toml
listen = "127.0.0.1:8787"
base_url = "https://flyer.cnife.cn"
admin_key = "fly_..."
session_secret = "..."
default_ttl = "1h"
max_ttl = "720h"
max_upload_bytes = 10485760
rate_limit_per_hour = 60
cleanup_interval = "15m"
grace_period = "24h"
```

## 客户端

```bash
flyer config set server https://flyer.cnife.cn
flyer config set admin-key fly_...      # 只用于管理命令
flyer admin key create --name agent     # stdout 打印新的 publisher key
flyer config set key fly_...            # 发布时使用
```

配置在 `~/.config/flyer/config.json`，权限 0600。

### 发布

```bash
flyer publish report.html
flyer publish report.html --ttl 24h
flyer publish report.html --permanent
flyer publish report.html --password secret
cat report.html | flyer publish -
flyer publish report.html --json
```

stdout 只输出 URL，方便管道；元信息和二维码写到 stderr。终端里默认显示二维码，`--no-qr` 关闭，`--qr` 强制显示。

`FLYER_SERVER`、`FLYER_KEY`、`FLYER_ADMIN_KEY`、`FLYER_PASSWORD` 可覆盖配置。

### 管理

```bash
flyer list                       # 列出当前 key 上传的页面
flyer list --limit 50 --offset 50
flyer open <slug>                # 在默认浏览器中打开仍有效的页面
flyer expire <slug>...           # 让页面立即失效
flyer delete <slug>...           # 立即删除页面
flyer admin key create --name agent
flyer admin key list
flyer admin key revoke 3
flyer admin stats
flyer admin cleanup
```

`list` 输出表格，`--json` 输出服务端结构。publisher key 只能看到和操作自己的页面；admin key 可以看到并操作所有页面。`expire` 后页面在 24 小时宽限期内返回 `410 Gone`，之后由后台清理删除；需要立即删除时用 `delete`。

`open` 会先用 HEAD 确认页面仍有效，再用系统默认浏览器打开；已过期或不存在时直接报错，不启动浏览器。密码保护页会打开解锁页。

## HTTP API

发布：

```bash
curl -X POST https://flyer.cnife.cn/api/pages \
  -H "Authorization: Bearer $FLYER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"html":"<h1>hi</h1>","title":"Hi","ttl_seconds":3600}'
```

响应：

```json
{
  "slug": "Ab3dE9xZ",
  "url": "https://flyer.cnife.cn/Ab3dE9xZ",
  "expires_at": "2026-11-03T11:00:00Z",
  "protected": false
}
```

页面管理：

```bash
# 列出当前 key 的页面（admin key 列出全部）
curl "https://flyer.cnife.cn/api/pages?limit=100&offset=0" \
  -H "Authorization: Bearer $FLYER_KEY"

# 让页面立即失效
curl -X POST "https://flyer.cnife.cn/api/pages/Ab3dE9xZ/expire" \
  -H "Authorization: Bearer $FLYER_KEY"

# 立即删除
curl -X DELETE "https://flyer.cnife.cn/api/pages/Ab3dE9xZ" \
  -H "Authorization: Bearer $FLYER_KEY"
```

`GET /api/pages` 按 `created_at DESC, slug DESC` 排序，`limit` 默认 100、最大 500，`offset` 默认 0。响应包含 `pages`、`total`、`limit`、`offset`；永久页面的 `expires_at` 省略。

管理端点（需要 admin key）：

- `GET /api/admin/stats`
- `POST /api/admin/cleanup`
- `GET /api/admin/keys`
- `POST /api/admin/keys`
- `DELETE /api/admin/keys/{id}`

`DELETE /api/pages/{slug}` 和 `POST /api/pages/{slug}/expire` 允许页面所有者或 admin 操作；已过期页面再次 expire 返回 `409`。

## 行为

- 默认 TTL 1 小时，最大 30 天；`permanent` 永不过期。
- 单文件上限 10 MiB，每个 key 每小时 60 次上传。
- 密码用 argon2id 哈希；解锁 cookie 1 小时，且不超过页面剩余寿命。
- 过期后 24 小时内返回 `410 Gone`，之后返回 `404`；后台每 15 分钟清理。
- 页面统一带 `X-Robots-Tag: noindex, nofollow, noarchive`，`/robots.txt` 禁止抓取。
- 页面 URL 是根路径 `/<slug>`，不是 `/<slug>.html`。
- 服务端监听 `127.0.0.1`，不要直接暴露到公网。

## 发布（维护者）

完整流程见 [RELEASING.md](RELEASING.md)。核心约束：

- GitHub Release 资产必须先生成；仓库必须保持 public。
- tag 必须与 `npm/package.json` 的版本一致。
- npm 使用 OIDC staged publish，维护者 2FA 批准后公开；不使用 `NPM_TOKEN`。
- 已发布 tag 不移动、不删除；坏版本发补丁版并 `npm deprecate`。

npm 包在 `npm/`，首次运行按平台从 GitHub Releases 下载原始二进制，校验 `checksums.txt` 后缓存。

## License

MIT。见 [LICENSE](LICENSE)。
