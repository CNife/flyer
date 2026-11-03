# flyer

把本地 HTML 发布成可分享的网页。默认 1 小时过期，也可以永久；支持密码保护、二维码和命令行管理。

服务端是单个 Go 二进制，页面存 SQLite + 本地文件。公网入口由 Caddy 终止 TLS 并反代到 `127.0.0.1:8787`。

## 构建

```bash
go build -o dist/flyer ./cmd/flyer
# 或
make build
```

## 服务端部署

```bash
# 系统用户和目录
sudo useradd --system --home /opt/flyer --shell /usr/sbin/nologin flyer
sudo install -d -o flyer -g flyer -m 0750 /opt/flyer

# 二进制
sudo install -o flyer -g flyer -m 0750 dist/flyer /opt/flyer/flyer

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
flyer admin key create --name agent
flyer admin key list
flyer admin key revoke 3
flyer admin stats
flyer admin cleanup
flyer delete <slug>
```

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

管理端点（需要 admin key）：

- `GET /api/admin/stats`
- `POST /api/admin/cleanup`
- `GET /api/admin/keys`
- `POST /api/admin/keys`
- `DELETE /api/admin/keys/{id}`
- `DELETE /api/pages/{slug}`（publisher 只能删自己的页面）

## 行为

- 默认 TTL 1 小时，最大 30 天；`permanent` 永不过期。
- 单文件上限 10 MiB，每个 key 每小时 60 次上传。
- 密码用 argon2id 哈希；解锁 cookie 1 小时，且不超过页面剩余寿命。
- 过期后 24 小时内返回 `410 Gone`，之后返回 `404`；后台每 15 分钟清理。
- 页面统一带 `X-Robots-Tag: noindex, nofollow, noarchive`，`/robots.txt` 禁止抓取。
- 页面 URL 是根路径 `/<slug>`，不是 `/<slug>.html`。
- 服务端监听 `127.0.0.1`，不要直接暴露到公网。

## 开发

```bash
go test ./...
go vet ./...
```
