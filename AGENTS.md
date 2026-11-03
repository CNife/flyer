# flyer 仓库规则

## 发版

- 发版前读 `RELEASING.md`。发版是公开操作，没有明确授权不要 push tag、发布 npm 或修改仓库可见性。
- 仓库必须保持 public。npm installer 匿名下载 GitHub Release 资产；转 private 前先把二进制迁到公开源并更新 installer。
- npm 只用 OIDC staged publishing。禁止添加 `NPM_TOKEN`，禁止直接 `npm publish`。
- tag 必须打在合并后的 main 上，`npm/package.json` 版本必须与 tag 一致。已发布的 tag 不移动、不删除。
- PR 合并和 `npm stage approve` 由维护者完成；除非维护者当场明确授权，agent 不代做，也不索取或代输 2FA 代码。agent 可以准备分支、跑测试、创建 PR、观察 CI、验证产物。
- 发版前跑 `go test ./...`、`go vet ./...`、`cd npm && npm test`。
- 发布后验证 workflow 成功、GitHub Release 资产、`npm view` 和 `npx @cnife/flyer@<version> version`。
