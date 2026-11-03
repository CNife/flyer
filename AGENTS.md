# flyer 仓库规则

发版、修改发布配置或仓库可见性前，必须先读并遵守 [RELEASING.md](RELEASING.md)。

提交前跑 `just check`；`justfile` 是本地与 CI 共用的任务入口，CI 见 `.github/workflows/ci.yml`。

## Agent skills

### Issue tracker

Issues live as GitHub issues in `CNife/flyer`, managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root plus `docs/adr/`. See `docs/agents/domain.md`.
