## Agent skills

### Issue tracker

Issues and specs live in this repo's GitHub Issues (via the `gh` CLI). See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical triage roles use their default label strings. See `docs/agents/triage-labels.md`.

### Domain docs

single-context: one repo-wide `GLOSSARY.md` at the root, architecture decisions in `docs/adr/`. See `docs/agents/domain.md`.

## 依赖策略（2026-11-11 维护者拍板，全仓生效）

**新增依赖不必逐次请示**，但**必须承担删除义务**：删功能、换实现、或某依赖已无任何代码路径使用后，**同一次变更里就要把它删掉**，不允许"留着以后可能用"。

**判定"没用了"的判据（双确认，缺一不可）**：

1. `go mod tidy` 之后 `go.mod` 里不再出现该 `require`；
2. `go list -deps ./...` 的输出里不再包含该包路径。

**执行纪律**：

- 删除依赖时，在 commit / PR 说明里写明**删了哪个依赖、为什么删**，不要静默去掉。
- 不要用 `// indirect` 状态当作"没用了"的判据——indirect 只表示不是直接 import，可能仍被自己的代码间接依赖。
- 不要手改 `go.sum`；一律走 `go mod tidy` 或 `go get` 生成。

> 本次首个适用案例：`github.com/santhosh-tekuri/jsonschema/v6` v6.0.3（Draft 2020-12、Apache-2.0、无 CGO），用于教务回传 JSON 的 schema 校验；若将来该校验被替换或移除，须按上述判据删除。
