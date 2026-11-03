# flyer 发版流程

面向维护者和 agent。发版会产生公开产物，没有明确授权不要 push tag、发布 npm，或修改仓库可见性。

## 不变量

- 仓库必须保持 public。`npm/lib/install.js` 匿名下载 GitHub Release 资产；private 仓库会返回 404。要转 private，先把二进制迁到公开源并更新 installer。
- `npm/package.json` 的 `version` 必须等于 tag 去掉 `v` 后的版本。
- npm 只用 OIDC staged publishing。禁止添加 `NPM_TOKEN`，禁止直接 `npm publish`。
- GitHub Release 资产必须先于 npm 包可用；npm 包只是下载二进制的 launcher。
- 已发布 tag 不移动、不删除。坏版本发补丁版并 `npm deprecate`。
- PR 合并和 `npm stage approve` 由维护者完成；除非维护者当场明确授权，agent 不代做。agent 不索取、不代输 2FA 代码。

## 发布链路

```text
main commit
   │
   └─ tag vX.Y.Z
        │
        └─ GitHub Actions
             ├─ release job
             │    ├─ go test / go vet
             │    ├─ GoReleaser → GitHub Release
             │    │    ├─ 原始二进制
             │    │    ├─ tar.gz / zip
             │    │    └─ checksums.txt
             │    └─ GHCR image
             └─ npm job
                  ├─ npm test
                  ├─ 校验 tag 与 package.json 版本
                  ├─ 版本已存在？跳过发布
                  └─ npm stage publish（OIDC）
                       │
                       └─ 维护者 2FA approve
                            │
                            └─ npm registry
```

## 发版前

1. 确认仓库公开：

   ```bash
   gh repo view CNife/flyer --json isPrivate,visibility
   ```

2. 从干净的 main 开始：

   ```bash
   git switch main
   git pull --ff-only
   git status --short
   ```

3. 在分支上修改 `npm/package.json` 的 `version`。版本提交走 PR，不在 CI 里改写版本。

4. 本地校验：

   ```bash
   just ci
   cd npm
   npm test
   npm pack --dry-run
   ```

5. 确认目标版本尚未发布。新版本应返回 404：

   ```bash
   VERSION=0.2.0          # 替换为本次版本
   npm view @cnife/flyer@"$VERSION" version
   ```

6. 合并版本 PR。tag 必须打在合并后的 main 上。

## 发版

```bash
set -euo pipefail
VERSION=0.2.0          # 替换为本次版本
TAG="v${VERSION}"

git switch main
git pull --ff-only
test "$(node -p 'require("./npm/package.json").version')" = "$VERSION"

# 这一步是公开操作；只有维护者明确授权本次发布才继续
CONFIRM=""
read -r -p "Push ${TAG}? type yes after maintainer authorization: " CONFIRM || true
[ "$CONFIRM" = "yes" ] || { echo "aborted: release not authorized" >&2; exit 1; }

git tag -a "$TAG" -m "$TAG"
git push origin "$TAG"
```

触发后观察 workflow：

```bash
gh run list --repo CNife/flyer --limit 5
gh run watch <run-id> --repo CNife/flyer --exit-status
```

确认 GitHub Release 资产：

```bash
gh release view "$TAG" --repo CNife/flyer --json assets --jq '.assets[].name'
gh release view "$TAG" --repo CNife/flyer --json assets --jq '.assets | length'
```

当前配置预期是 13 个资产：6 个原始二进制、6 个压缩包、1 个 `checksums.txt`。如果 `.goreleaser.yaml` 增减平台，数量会变；以实际 `gh release view` 结果为准。

## npm staged 批准

CI 成功后，npm 包处于 staged 状态，尚未公开。`npm stage` 需要 npm >= 11.15；workflow 固定使用 11.21.1，本地 npm 过旧会报 unknown command：

```bash
npm stage list
npm stage view <stage-id>
```

等待 `status` 从 `validating` 变为 `staged`。然后由维护者用 2FA 批准：

```bash
npm stage approve <stage-id>
```

稳定版批准后 `latest` 指向新版本；预发布 tag（含 `-`，如 `v0.2.0-rc.1`）发布到 `next`，`latest` 不变。用以下命令确认：

```bash
npm view @cnife/flyer dist-tags --json
```

agent 不索取、不代输 OTP。遇到 `EOTP` 时交给维护者交互完成。

## 发布后验证

```bash
npm view @cnife/flyer version
npm view @cnife/flyer@"$VERSION" dist.attestations --json

mkdir -p /tmp/flyer-release-check
cd /tmp/flyer-release-check
npm init -y
npm install @cnife/flyer@"$VERSION"
npm audit signatures

FLYER_CACHE_DIR=/tmp/flyer-cache-check ./node_modules/.bin/flyer version
npx -y @cnife/flyer@"$VERSION" version
```

预期：

- 稳定版 `latest` 指向新版本；预发布版本只移动 `next`，`latest` 不变。
- public repo + OIDC 发布会有 provenance attestation；0.1.0 是本地 staged 发布，没有 provenance，0.1.1 起有。
- `npm audit signatures` 显示 verified registry signature 和 verified attestation。
- 两个 CLI 命令都输出 `flyer vX.Y.Z`。

## 首次配置（已有包可跳过）

```bash
npm login
npm trust github @cnife/flyer \
  --file release.yml \
  --repo CNife/flyer \
  --allow-stage-publish
```

也可以在 npmjs.com → 包 → Settings → Trusted Publisher 配置。配置后在 Publishing access 里选择 **Require 2FA and disallow tokens**。这些配置步骤由维护者执行。

## 故障恢复

| 症状 | 原因 | 处理 |
|---|---|---|
| `checksums.txt` 404 | Release 不存在，或仓库是 private | 先补 Release / 改 public；不要先发 npm |
| workflow 报 version mismatch | tag 与 `npm/package.json` 不一致 | 发新的补丁版本；不要移动旧 tag |
| `npm stage approve` 返回 `EOTP` | 需要交互式 2FA | 交给维护者在终端完成 |
| staged 状态是 `validating` | 自动审核未完成 | 等待变为 `staged` 再批准 |
| 刚发布后 `npm install` 报 `ETARGET` | registry 传播延迟 | 等 30–60 秒，用 `--prefer-online` 重试；不要重复发版 |
| npm 报版本已存在 / `EPUBLISHCONFLICT` | 版本已发布 | bump patch；不要复用 tag |
| workflow 在 GoReleaser 后失败 | Release 可能已经创建 | 先 `gh release view` 检查资产；不要创建重复 Release |
| tag 触发时 workflow 仍是 disabled | tag 与 workflow 状态操作并发 | 先 `gh workflow list --all` 确认 active，再 push tag |
| 旧 workflow 报 `Set npm version` 失败 | v0.1.0 的历史流程 | 当前流程只校验版本；不要重跑旧 tag |
| 已发布版本有问题 | 版本不可可靠撤回 | `npm deprecate @cnife/flyer@<version> "reason"`，然后发补丁版 |

## Retro

- 0.1.0 先发了 npm、后补 GitHub Release，且仓库当时是 private，导致 launcher 下载 `checksums.txt` 404。教训：Release 资产是 npm 包契约的一部分，仓库可见性也是分发的一部分。
- 0.1.0 的旧 workflow 在 `Set npm version` 步骤失败，因为 CI 试图把版本改成已经是 0.1.0 的值。现在改为校验 tag 和 package.json，不在 CI 改写版本。
- 0.1.1 通过 OIDC staged publish + 维护者 2FA 批准，产出了 provenance；`npm audit signatures` 验证通过。
- registry 发布后可能有短暂传播延迟；`npm view` 和 `npm install` 不要用一次 404 判断发布失败。
