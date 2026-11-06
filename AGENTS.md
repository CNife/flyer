# flyer 仓库规则

发版、修改发布配置或仓库可见性前，必须先读并遵守 [RELEASING.md](RELEASING.md)。

提交前跑 `just check`；`justfile` 是本地与 CI 共用的任务入口，CI 见 `.github/workflows/ci.yml`。

推送验证走 no-mistakes 门禁：每个 clone 先 `no-mistakes init`，再用 `git push no-mistakes <branch>` 或 `/no-mistakes` 验证已提交的分支。门禁命令和审查策略在 `.no-mistakes.yaml`，保持与 `justfile` 一致，不要另建检查入口。

## 测试策略

- 默认写端到端测试：构建真实二进制，启动只监听 `127.0.0.1` 的临时服务端，从 CLI/HTTP 边界验证用户可见行为。
- 单元测试只补 E2E 无法稳定制造的缺口：注入时钟、确定随机数、内部错误分支、并发边界。不要回填测试替身来复述生产代码的组装逻辑。
- `just test` 包含 `test/e2e`；新增测试优先复用 `test/e2e/harness_test.go`，不要另建测试入口。
- no-mistakes 是推送前的早反馈，CI 是权威门；本地通过不能成为跳过 CI 测试的理由。

## Agent skills

### Issue tracker

Issues live as GitHub issues in `CNife/flyer`, managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root plus `docs/adr/`. See `docs/agents/domain.md`.
