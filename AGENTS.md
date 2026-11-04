# flyer 仓库规则

发版、修改发布配置或仓库可见性前，必须先读并遵守 [RELEASING.md](RELEASING.md)。

提交前跑 `just check`；`justfile` 是本地与 CI 共用的任务入口，CI 见 `.github/workflows/ci.yml`。

推送验证走 no-mistakes 门禁：每个 clone 先 `no-mistakes init`，再用 `git push no-mistakes <branch>` 或 `/no-mistakes` 验证已提交的分支。门禁命令和审查策略在 `.no-mistakes.yaml`，保持与 `justfile` 一致，不要另建检查入口。

## Agent skills

### Issue tracker

Issues live as GitHub issues in `CNife/flyer`, managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root plus `docs/adr/`. See `docs/agents/domain.md`.
