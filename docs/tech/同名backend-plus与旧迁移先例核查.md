# 同名 `apps/nongyu-backend-plus` 目录与旧「迁移」先例核查

> 本文是 `/research` 产物：**只列一手事实、只做事实核查，不判断迁移方案、不拍板**。选址：与事实底稿 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md` 同放 `docs/tech/`，因为 `@NEW/README.md:34` 约定该目录放「技术方案（需要时）」，本核查是给后续 `/to-spec` 交 Spec 收口票用的仓内事实底稿。[来源: @NEW/README.md:32-35；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:5]

## 调研范围与来源

本次只读磁盘目录与仓内 git 元数据，未访问网络、未引用任何二手转述，未调用 `deck_*` 工具，未创建 issue / 分支，未做 `git commit`，除本文件外未改动任何文件。

- **`@OLD/`**（旧农屿 monorepo 根）：`D:\IT\program\web-program\nongyu-system`。[来源: 本会话 `Get-ChildItem -LiteralPath 'D:\IT\program\web-program\nongyu-system' -Force` 输出（目录列举，无行号）]
- **`@NEW/`**（本仓、当前工作目录）：`D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus`。[来源: 本会话运行时上下文 `Current working directory`；`Resolve-Path` 输出]
- **关键路径关系**：`@OLD/apps/nongyu-backend-plus` 与 `@NEW/` 指向**同一物理目录**（`@NEW` 是 `@OLD` 的 `apps/` 子目录）。证据：`Resolve-Path -LiteralPath '...@OLD\apps\nongyu-backend-plus'` 与 `Get-Location` 相等返回 `True`；`git -C @NEW rev-parse --show-toplevel` 输出 `D:/IT/program/web-program/nongyu-system/apps/nongyu-backend-plus`；该路径 `Attributes=Directory`，`LinkType` 为空、`Target` 为空（不是 junction / 符号链接）。[来源: 本会话 pwsh `Resolve-Path`/`Get-Item` 与 git `rev-parse --show-toplevel` 输出（命令输出，无行号）]
- 事实底稿把 `@NEW/` 与 `@OLD/` 写成「本次只读两处磁盘目录」，并称 `@OLD/apps/nongyu-backend-plus`「与本仓库同名但是不同实体」「内容未读、需另行核对」。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:9-12,123,204] 本文即补这次核对；核对结果与「不同实体」的措辞有出入，见下文（只做事实澄清，不改底稿文件）。

**引用约定**：能落到具体文件的按 `[来源: @OLD/路径:行号]` / `[来源: @NEW/路径:行号]`；目录列举与 `git` 命令输出本身没有行号，按 `[来源: 本会话 <完整命令> 输出（无行号）]` 标注，命令与参数写全以便复核。git 元数据文件（`.git/config`、`.git/HEAD`）有行号，按文件引用。`@OLD/` 下被 `.gitignore` 排除的敏感文件（服务器连接信息等）本次未读。

---

## 同名目录核查

### 目录清单（一手列举）

- `@OLD/` 根含 `.git`（目录）、`.github`、`.husky`、`apps`、`docs`、`packages`、`scripts`、`node_modules`、`.gitignore`、`pnpm-workspace.yaml`、`package.json`、`AGENTS.md`、`README.md` 等。[来源: 本会话 `Get-ChildItem -LiteralPath 'D:\IT\program\web-program\nongyu-system' -Force` 输出（无行号）]
- `@OLD/apps/` 共 8 项：`nongyu-app-plus`、`nongyu-backend-plus`、`nongyu-go-track-server`、`nongyu-node-server`、`nongyu-node-track-server`、`nongyu-rn-app`、`nongyu-web-admin`、`nongyu-web-site`。[来源: 本会话 `Get-ChildItem -LiteralPath 'D:\IT\program\web-program\nongyu-system\apps' -Force` 输出（无行号）；与 @NEW/docs/tech/旧农屿WebVPN迁移调研.md:120 的列举一致]
- `@OLD/apps/nongyu-backend-plus/`（即 `@NEW/` 根，force 列举）共 12 项：隐藏目录 `.git`、`.idea`，目录 `cmd`、`deploy`、`docs`、`internal`、`migrations`，文件 `.env.example`、`.gitignore`、`AGENTS.md`、`go.mod`、`go.sum`、`Makefile`、`README.md`。[来源: 本会话 `Get-ChildItem -LiteralPath 'D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus' -Force` 输出（无行号）]
- 该目录 `.git` 是**真实目录**（不是 gitfile）：内含 `hooks`、`info`、`logs`、`objects`、`refs` 及 `COMMIT_EDITMSG`、`config`、`description`、`HEAD`、`index`。[来源: 本会话 `Get-ChildItem -LiteralPath '...\nongyu-backend-plus\.git' -Force` 输出（无行号）]
- 全局检索 `package.json`：`glob "**/package.json" @NEW` = **0 命中**。[来源: 本会话 `glob **/package.json` 输出（无行号）]
- `@OLD/` 根**没有** `.gitmodules` 文件（`Test-Path` 返回 `False`）；`git -C @OLD ls-files --stage -- 'apps/nongyu-backend-plus'` 输出为空。[来源: 本会话 pwsh `Test-Path` 与 git `ls-files --stage` 输出（无行号）]

### 内容判定

**判定：`@OLD/apps/nongyu-backend-plus` 既不是本仓的「前身」、也不是「另一个骨架」、也不是「占位空目录」——它就是本仓（`@NEW/`）本身，是同一个物理工作树；它同时是嵌套在旧 monorepo `apps/` 下的一个独立 Git 仓库。** 事实链：

1. **同一物理目录**：见上文 `Resolve-Path` / `rev-parse --show-toplevel` / 非 junction 三项证据。[来源: 本会话 pwsh `Resolve-Path`、`Get-Item` 与 git `rev-parse --show-toplevel` 输出（无行号）]
2. **两个不同的 Git 仓库上下文**：
   - 嵌套库 `@NEW/.git/HEAD:1` = `ref: refs/heads/main`；`@NEW/.git/config:8-10` 的 `remote "origin".url = https://github.com/NongyuSystem/nongyu-backend-plus.git`。[来源: @NEW/.git/HEAD:1；@NEW/.git/config:8-10]
   - 父库 `@OLD/.git/config:11-13` 的 `remote "origin".url = https://github.com/NongyuSystem/nongyu-system.git`。[来源: @OLD/.git/config:11-13]
   - 两个 remote 指向不同 GitHub 仓库；`git -C @NEW log` 与 `git -C @OLD log` 是彼此独立的历史。[来源: 本会话 `git -C ... remote -v` 与 `git -C ... log --oneline` 输出（无行号）]
3. **嵌套独立仓库是一手明写的事实，不是推测**：
   - `@OLD/.gitignore:95` 注释「新农屿后端：独立 Git 仓库（嵌套在 apps/ 下，由自身仓库管理，父仓库不跟踪）」，`:96` 为 `apps/nongyu-backend-plus/`。[来源: @OLD/.gitignore:95-96]
   - `@OLD/docs/common/项目结构总览.md:52` 把该目录标为「新农屿后端 Go 服务（**独立 Git 仓库**，父仓库不跟踪；模块化单体 + Gin）」；`:55` 「新农屿相关目录…为独立演进的新工程，不复用旧农屿后端实现」；`:56` 「虽位于 `apps/` 下，但**自身是独立 Git 仓库**，已在根 `.gitignore` 中排除；其文档随自身仓库 `docs/` 走」。[来源: @OLD/docs/common/项目结构总览.md:52,55-56]
   - 父库确实不跟踪：`git -C @OLD ls-files 'apps/nongyu-backend-plus'` 与 `ls-files --stage` 均为空，`git -C @OLD status --porcelain -- 'apps/nongyu-backend-plus'` 为空；且无 `.gitmodules`（**不是 submodule**）。[来源: 本会话 `git -C @OLD ls-files`/`ls-files --stage`/`status --porcelain` 输出与 `Test-Path .gitmodules`（无行号）；@OLD/.gitignore:95-96]
4. **技术栈与状态 = 本仓 Go 骨架本身**：
   - `@NEW/go.mod:1` `module nongyu-backend-plus`，`:3` `go 1.26`，`:5` `require github.com/gin-gonic/gin v1.11.0`。[来源: @NEW/go.mod:1,3,5]
   - `@NEW/README.md:7` 「骨架阶段。仅提供可编译、可启动、可优雅关停的最小工程结构 + `GET /healthz`，**不含任何业务逻辑、不连接数据库**」。[来源: @NEW/README.md:7]
   - `@NEW/internal/server/server.go:12-13` 注释「骨架阶段只挂 /healthz；业务路由由 app 装配层在拿到各自 handler 后注册」；`:18` 只注册 `r.GET("/healthz", healthz)`；`:24-27` 返回 `{"status":"ok"}`。[来源: @NEW/internal/server/server.go:12-13,18,24-27]
   - `@NEW/cmd/server/main.go:1-4` 「唯一可执行程序」「只做三件事：读配置 → 装配 App → 启动并优雅关停」「业务逻辑一律不写在这里」。[来源: @NEW/cmd/server/main.go:1-4]
   - 该目录**没有** `package.json`（0 命中），也没有 `docs/adr/` 目录（`Test-Path` 为 `False`）。[来源: 本会话 `glob **/package.json` 与 `Test-Path .../docs/adr`（无行号）]
5. **嵌套库自身的历史**：`git -C @NEW log --format='%h %ad %s' --date=short` 只有 3 个提交，均为 2026-11-06：`35e033e feat(init): 初始化新农屿后端 Go 服务骨架（模块化单体 + Gin + /healthz）`、`28bdcf1 chore: 忽略 DSH 插件运行时产物 docs/agents/`、`95c41fb chore(setup): 初始化 mattpocock skills 配置（GitHub Issues + 默认 triage + single-context）`；HEAD = `95c41fb`。首个提交 `35e033e` 的 `ls-tree -r --name-only` 共 18 个文件，全是新建骨架文件（`.env.example`、`.gitignore`、`Makefile`、`README.md`、`cmd/server/main.go`、`deploy/.gitkeep`、`docs/{plans,specs,tech}/.gitkeep`、`go.mod`、`go.sum`、`internal/{app,config,platform,server}/*`、`migrations/.gitkeep`），无任何旧后端代码文件。[来源: 本会话 `git -C @NEW log --format=...`、`git -C @NEW show --stat --oneline 35e033e`、`git -C @NEW ls-tree -r --name-only 35e033e` 输出（无行号）]
6. **自身 `.gitignore` 与父库各自独立**：`@NEW/.gitignore:1-35` 是 Go 项目规则（`/bin/`、`*.exe`、`go.work`、`docs/agents/` 等），`:30-32` 注明「本项目为单一 module，不使用 go.work」；父库 `@OLD/.gitignore:1-96` 是 Node/pnpm monorepo 规则。两者内容不同，进一步印证是两套仓库各自的忽略规则。[来源: @NEW/.gitignore:1-35；@OLD/.gitignore:1-96]

### 与 `@NEW/README.md:3` 的一手证据对照

`@NEW/README.md:3` 原文：「新农屿后端的 Go 服务。**独立于旧农屿（`nongyu-system` monorepo）的全新仓库**，不复用旧后端代码、表结构与部署链路。」[来源: @NEW/README.md:3]

| README:3 的断言 | 一手证据 | 是否冲突 |
| --- | --- | --- |
| 「独立于旧农屿 monorepo」 | 该目录自带 `.git/`，remote 为 `nongyu-backend-plus.git`，与父库 `nongyu-system.git` 不同；父库 `.gitignore:95-96` 明写「独立 Git 仓库…父仓库不跟踪」；`项目结构总览.md:52,56` 同样明写；`git -C @OLD ls-files` 为空。[来源: @NEW/.git/config:8-10；@OLD/.git/config:11-13；@OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,56；本会话 `git -C @OLD ls-files` 输出（无行号）] | **不冲突，相互印证**。「独立」指 Git 仓库独立，不是「磁盘上另有一个旧同名目录」。 |
| 「全新仓库」 | 嵌套库首个提交即「初始化新农屿后端 Go 服务骨架」，18 个文件全为新建，3 个提交全在 2026-11-06。[来源: 本会话 `git -C @NEW log`/`show --stat 35e033e`/`ls-tree 35e033e` 输出（无行号）] | **不冲突，相互印证**。 |
| 「不复用旧后端代码」 | 目录内无 `package.json`（0 命中）；技术栈 Go 1.26 + Gin（`@NEW/go.mod:1,3,5`），而旧后端为 Node/Express（`@OLD/docs/common/项目结构总览.md:48`）与 Node/Fastify 埋点（`@OLD/docs/common/项目结构总览.md:49`）；`项目结构总览.md:55` 明写「不复用旧农屿后端实现」；`@NEW/README.md:84` 「新农屿基本不复用以前的后端实现」。[来源: 本会话 `glob **/package.json` 输出（无行号）；@NEW/go.mod:1,3,5；@OLD/docs/common/项目结构总览.md:48-49,55；@NEW/README.md:84] | **不冲突，相互印证**。 |
| 「不复用…表结构与部署链路」 | `@NEW/README.md:7` 自述骨架期「不连接数据库」、`migrations/.gitkeep` 与 `deploy/.gitkeep` 为空占位；`@NEW/README.md:30-39` 目录约定也为空占位。[来源: @NEW/README.md:7,30-39；本会话 `git -C @NEW ls-tree 35e033e`（含 `migrations/.gitkeep`、`deploy/.gitkeep`，无行号）] | **不冲突**（仓内尚无表结构与部署链路可复用）。 |

**对照结论（事实）**：`@OLD/apps/nongyu-backend-plus` 与 `@NEW/` 在磁盘上是**同一目录**，因此底稿 :123 「同名但是不同实体」若理解为「两个磁盘目录/两个代码库内容」则不成立；若理解为「两个 Git 仓库上下文」（父 monorepo 仓库 vs 嵌套独立仓库）则与 `.gitignore:95-96`、`项目结构总览.md:52,56` 一致。README:3 的「独立 / 全新 / 不复用」在仓内一手材料中**均得到印证，未发现冲突证据**。[来源: @NEW/README.md:3,84；@OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,55-56；本会话 `Resolve-Path`/`git rev-parse`/`git remote -v` 输出（无行号）]

> 附带的口径提示（事实，不改底稿）：事实底稿 :11-12 把 `@NEW/` 与 `@OLD/` 并列成「两处磁盘目录」，实际是包含关系（`@NEW` ⊂ `@OLD`）；:9 「只读两处磁盘目录」按实际应理解为「一个目录树的两个层级 + 两套 git 仓库元数据」。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:9-12]

---

## 旧迁移先例的文档链

那次迁移的**自述定义**：「需要将 **同一职责、同一机器、同一 HTTP 契约** 的实现语言从 Go 换为 Node，且 **永不** 与业务 Node 同机合并」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:20] 状态「**已确认**；实施中」。[来源: 同文件:12]

### 前置文档清单（仓内一手，按文档自述的依赖顺序）

以下顺序取自各文档表头「依据 / 关联 / 依赖」与状态栏，**是文档自述的依赖链，不是提交时间顺序**（本次未逐 commit 追溯 git 历史）：

| # | 文档 | 角色与状态（原文要点） | 来源 |
| --- | --- | --- | --- |
| 1 | `docs/forhuman/rawprds/nongyu-rn-app/User/埋点及用户交互PRD.md` | 原始 PRD；一期 Spec 表头「PRD」指向它 | [来源: @OLD/docs/forhuman/rawprds/nongyu-rn-app/User/埋点及用户交互PRD.md:1；@OLD/docs/nongyu-go-track-server/specs/埋点服务一期.md:7] |
| 2 | `docs/nongyu-go-track-server/specs/埋点服务一期.md` | 一期 Spec；「行为语义继承」来源；状态「AI 编码完成 · go test 已通过 · 待人工验收」 | [来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务一期.md:1,11；@OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:11] |
| 3 | `docs/nongyu-go-track-server/接口文档.md` | 「**HTTP 契约真相**」「实现须 100% 对齐」；字段级不重复抄写 | [来源: @OLD/docs/nongyu-go-track-server/接口文档.md:1,7；@OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:10,66,83] |
| 4 | `docs/nongyu-go-track-server/tech/埋点服务技术方案.md` | 一期技术方案（库表 DDL / 容量 / 契约依据） | [来源: @OLD/docs/nongyu-go-track-server/tech/埋点服务技术方案.md:1,3-6；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:10] |
| 5 | `docs/nongyu-go-track-server/技术选型.md` | 一期技术底座选型 | [来源: @OLD/docs/nongyu-go-track-server/技术选型.md:1,8-9；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:10] |
| 6 | `docs/nongyu-go-track-server/部署与发布.md` | 部署/发布现状；迁移后需「增补 Node 现行发布说明」；状态「现行实现为 Node」 | [来源: @OLD/docs/nongyu-go-track-server/部署与发布.md:1,5,8；@OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:169] |
| 7 | `docs/nongyu-go-track-server/tech/埋点迁移压测报告.md`（另有一份 `埋点迁移压测报告-full-node.md`） | 2026-08-29 Track 独立机写入压测，「验证『千人规模写入』下，用 Node+SQLite 替换现网 Go Track 是否资源可接受」；迁移决策依据 | [来源: @OLD/docs/nongyu-go-track-server/tech/埋点迁移压测报告.md:1,5-9；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:10,42-46；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:100；目录列举见本会话 `Get-ChildItem -Recurse` 输出（无行号）] |
| 8 | `docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md` | **迁移技术方案**，状态「**决策已锁定**；Spec/计划已确认；**实施中**」；含「部署铁律」「迁移原则」「决策锁定」§12 | [来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:1,5-11,15-23,48-57,342-354] |
| 9 | `docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md` | **迁移 Spec**（本次要查的入口文档），状态「已确认；实施中」；表头列「技术方案（决策已锁定）」「HTTP 契约真相」「原一期 Spec（行为语义继承）」 | [来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:1,3-12] |
| 10 | `docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md` | **实施计划**，状态「代码与 CD 已完成（A–E）；待 Phase F 机上切换」；表头「Spec（已确认）」「技术方案（决策已锁定）」；§8「确认前不开始编码」 | [来源: @OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:1,3-9,132-134] |
| 11 | `packages/nongyu-track-contract` + `apps/nongyu-node-track-server` | 共享契约包（TRACK_EVENT_TYPES 等）与 Node 新实现；RN Telemetry 与 Node Track 共同依赖 | [来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:6-7,85-92；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:34-51] |
| 12 | `.github/workflows/cd.yml`、`scripts/cd/publish-track.sh`、`scripts/deploy-nongyu-track.ps1`、`scripts/track-deploy.env.example`、unit 示例 | CD / 部署资产；技术方案 §13「参考」与计划 Phase E 列出 | [来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:360-366；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:72-78；@OLD/docs/nongyu-go-track-server/部署与发布.md:150-156] |
| 13 | `docs/common/项目结构总览.md` | 计划 §5「文档同步」要求增补新包名 | [来源: @OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:105-110；@OLD/docs/common/项目结构总览.md:49-50] |

- 流程自述：技术方案 §12 决策锁定表末行「**先 Spec（SDD）再编码**」；计划 §7「本计划经用户确认后编码」、§8「确认本计划无误后回复『通过』…**确认前不开始编码**」。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:354；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:125,134]
- **ADR**：`@OLD/docs/` 下**仓内未找到** `docs/adr/` 目录（`glob "docs/adr/**"` = 0 命中；`Test-Path` 为 `False`）。只有 `@OLD/docs/agents/domain.md:7,12,23` 提到约定「durable architecture decisions live in `docs/adr/`」并画出目录示意，但该目录在磁盘上不存在；部分 Spec 内有「## 决策记录（grill 结论）」小节（如 `@OLD/docs/nongyu-node-server/specs/广场回复与留言.md:299`）。该迁移链文档中**仓内未找到**被标为 ADR 的文档。[来源: 本会话 `glob docs/adr/**`、`Test-Path docs/adr`、`grep "ADR|adr|决策记录"` 输出（无行号）；@OLD/docs/agents/domain.md:7,12,23；@OLD/docs/nongyu-node-server/specs/广场回复与留言.md:299]
- 同目录另有 `docs/nongyu-go-track-server/plans/埋点服务一期.md`（一期计划）与 3 份后续 Spec（`dims页均停留与进入次数.md`、`LLM代理失败事件入库.md`、`在线峰值首次达到时刻.md`），本次只确认路径存在，未逐行读取其内容。[来源: 本会话 `Get-ChildItem -Recurse` 目录列举输出（无行号）]

### 切换与回滚怎么写

- **Spec §5.1 切换（摘要）**：`备份 track.db → Track 机安装对齐版本 Node + 依赖工具链 → 部署 Node Track（可先影子端口验证） → 维护窗口：停 Go → 启 Node（8081）→ Nginx 不变 → 探活 + App 抽样 + BFF overview → 观察 24–72h → CD 仅发 Node；Go 下线`。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:121-131]
- **Spec §5.2 回滚（切换窗口内）**：「systemd 指回 Go 二进制 + 原 env；若 Node 写坏数据则用切换前 db 备份覆盖（丢窗口内增量，需窗口内决策）」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:133-135]
- **技术方案 §7.1**：M1 契约实现 → M2 影子验证（另端口、**不切公网**）→ M3 切换（同一台 Track 机：停 Go → 确认 db 备份 → 启 Node 仍 8081 → Nginx 不变）→ M4 观察 24–72h → M5 收尾（Go 下线；CD 只发 Node；仓库与文档中的 Go 实现保留）。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:241-247]
- **技术方案 §7.2 回滚三步**：① `systemctl stop nongyu-node-track`；② 恢复 Go `ExecStart=/usr/local/bin/nongyu-track` + 原 env；③ 若数据写坏用切换前 backup 覆盖 `track.db`；并「建议 unit 名可继续叫 `nongyu-track.service`（只改 ExecStart）」。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:249-255]
- **技术方案 §7.3 客户端**：App `EXPO_PUBLIC_TRACK_BASE_URL`、Node `TRACK_BASE_URL` 可不变；「无需发版 App，只要服务端契约兼容」。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:257-260]
- **计划 Phase F**：M2 影子 / M3 切换（冷备 db → 停 Go → 启 Node:8081 → Nginx 不变）/ M4 观察 24–72h / M5 收尾；「回滚：systemd 指回 Go 二进制；必要时恢复切换前 db 备份」。[来源: @OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:80-89]
- **部署文档**：`部署与发布.md:31`「切换窗口内回滚 Go：改 unit `ExecStart` 指回 `/usr/local/bin/nongyu-track`」；`:169-179` §7「装机 / 切到 Node（摘要）」并指向「详细切换步骤见 `plans/埋点服务迁移至Node.md` Phase F」。[来源: @OLD/docs/nongyu-go-track-server/部署与发布.md:31,169-179]
- **验收与回滚时限**：Spec §6 完成定义含「生产流量由 Node Track 承载；Go 无线上流量」；技术方案 §11 第 5 条「故障可在 5 分钟内回滚到 Go」；计划 §4 CD 验收「仅改 Go 不发生产；改 Node Track 可走通发布+health 回滚」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:156-160；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:332-338；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:93-101]

### 哪些文档被标为「保留不删」

- 迁移 Spec §3 边界表：「删除 `apps/nongyu-go-track-server` 或清空其文档 → **保留**」；§2 目标 4「Go 代码与文档保留在仓库，但不再作为生产发布物」；§4.6「仓库保留 `apps/nongyu-go-track-server` 与既有文档；文档注明现行实现为 Node」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:29,40,114-115]
- 技术方案 §4.3「Go 代码与文档保留策略」表：线上「Go 埋点**下线**」、仓库「`apps/nongyu-go-track-server` **保留**（只读参考 / 历史实现），不删除」、文档「`docs/nongyu-go-track-server/**` **保留**；新增/主导文档标明『实现已迁 Node』」、CI「生产 CD 不再编译/发布 Go 二进制」；§8.4「仓库 **保留** …供对照与紧急回滚参考」。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:178-185,293-297]
- 计划 §6「明确不做」第一条「删除 Go 代码或文档」；§5 文档同步要求「`部署与发布.md`、必要时 `技术选型.md` 标明现行实现为 Node」。[来源: @OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:105-110,114-119]
- 落地标记：`apps/nongyu-go-track-server/DEPRECATED.md:6`「本目录用途：迁移对照、历史行为核对、紧急回滚参考（见 … 决议：**仓库保留、不删除**）」；`部署与发布.md:5`「Go 仓库保留供回滚对照」、`:211` 变更记录「2026-08-29 现行发布改为 Node Track；Go 仅回滚/对照」；`项目结构总览.md:50`「历史 Go 埋点实现（仓库保留；生产 CD 不再发布）」、`:64`「埋点契约与迁移文档仍集中于此；现行实现为 Node Track」。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:6；@OLD/docs/nongyu-go-track-server/部署与发布.md:5,211；@OLD/docs/common/项目结构总览.md:50,64]
- 压测脚本则**被删除**：`DEPRECATED.md:9`「压测脚本（`scripts/track-migration-bench/`）已随迁移完成清理删除」；报告归档到 `tech/埋点迁移压测报告*.md`。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:9；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:10]

### CD 怎么切

- **Spec §4.5**：path filter 改为 `apps/nongyu-node-track-server/**`（及 `packages/nongyu-track-contract/**`）；`deploy-track`「停止 Go 交叉编译；改为 pnpm filter 构建 Node `dist` 并发布到 Track 机」；`publish-track.sh` / `deploy-nongyu-track.ps1`「改为 Node 产物发布语义（健康检查与回滚保留）」；「Go 目录变更**不**再触发生产 Track 发布」；密钥「可继续使用现有 `TRACK_SSH_*` secrets」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:102-110]
- **技术方案 §8.2** 逐项列出改造资产表（`cd.yml` 的 `plan` path filter / `deploy-track` job / `publish-track.sh` / `deploy-nongyu-track.ps1` / systemd unit / 文档）；§8.3「**不再**因仅改动 `apps/nongyu-go-track-server/**` 而触发生产 Track 发布」「Go 目录若需 CI：可另加**非 deploy** 的可选 job（如 `go test`），与生产 CD 解耦」。[来源: @OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:275-291]
- **计划 Phase E**（A–E 已完成）：改 `.github/workflows/cd.yml`、改 `scripts/cd/publish-track.sh`、改 `scripts/deploy-nongyu-track.ps1` + `track-deploy.env.example`、`deploy/nongyu-track.service.example`「复用 unit 名，`ExecStart=<node> /opt/nongyu-track/dist/index.js`」、更新 `部署与发布.md`「现行 Node 章节；Go 标为历史/回滚」；并「**确认前不开始编码**」。[来源: @OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:72-78,134]
- **仓内现状（一手验证）**：`@OLD/.github/workflows/cd.yml:52` path filter 已是 `'apps/nongyu-node-track-server/**'`；`:197` `deploy-track:`；`:212` `test -f apps/nongyu-node-track-server/dist/index.js`；`:213` `tar -czf nongyu-track-dist.tgz -C apps/nongyu-node-track-server/dist .`；`:228` `scp apps/nongyu-node-track-server/package.deploy.json ...`。即 CD 已按 Node 语义落地，未见 Go 交叉编译条目。[来源: @OLD/.github/workflows/cd.yml:52,197,212-213,228]
- **部署文档**同样记录：`部署与发布.md:25`「日常 CD：path 变更触发 `deploy-track`：pnpm build → `publish-track.sh` 发布并 health 回滚」、`:42`「`main` 上 Node Track / 契约包有变更时，GitHub CD 构建并发布到 `/opt/nongyu-track`」、`:62`「因仅改动 `apps/nongyu-go-track-server/**` 而触发生产发布」列入非目标、`:211` 变更记录。[来源: @OLD/docs/nongyu-go-track-server/部署与发布.md:25,42,62,211]

### 状态自述不完全一致（如实记录，不推测原因）

- `DEPRECATED.md:3` 自称「本目录为**已下线**的 Go 埋点服务，生产 CD 不再发布」。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:3]
- 但迁移 Spec `:12`「已确认；实施中」、技术方案 `:5`「实施中」、计划 `:8`「代码与 CD 已完成（A–E）；**待 Phase F 机上切换**」、部署文档 `:199` 验收项 `[ ]` **未勾选**「**Node 替换 Go 切换完成**（Phase F；代码与 CD 已就绪）」。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:12；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:5；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:8；@OLD/docs/nongyu-go-track-server/部署与发布.md:199]
- 本次未追溯 git 提交时间线与机器上真实运行状态，故只记录文档自述差异，不判定哪一份是最新事实。[来源: 本会话范围说明]

---

## 与本 effort 的关系

**只列「哪些事实会进入 Spec 收口票」的提示，不做任何决定**：

1. 本 effort 的 `/to-spec` 收口票若要引用「仓内 SDD 链路先例」，可直接指向上表 13 项文档链（PRD → 一期 Spec / 接口文档 / 技术方案 / 选型 → 压测报告 → 迁移技术方案（决策锁定）→ 迁移 Spec（已确认）→ 实施计划（确认前不编码）→ 部署文档 / CD）。[来源: 本文档「旧迁移先例的文档链」各条]
2. 该先例中「HTTP 契约真相单列一份文档、字段级不重复抄写、实现须 100% 对齐」与「先 Spec（SDD）再编码」两条做法，与 `@NEW/README.md:79-80`「PRD → Spec → 技术方案（按需）→ 实施计划 → 编码 → 人工 Review」「禁止无规格直接写业务代码」并存；是否采用由维护者决定。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:10,66,83；@OLD/docs/nongyu-go-track-server/tech/Go埋点迁移至Node技术方案.md:354；@NEW/README.md:79-80]
3. `@OLD/apps/nongyu-go-track-server/DEPRECATED.md` 与 `@NEW/README.md:84` 的一致性结论、以及「仓库保留不删 / 压测脚本已删除」的处置先例，是事实，可进收口票的背景区。[来源: 见下节；@OLD/apps/nongyu-go-track-server/DEPRECATED.md:1-9]
4. 嵌套独立仓库的事实（`@OLD/.gitignore:95-96`、`@OLD/docs/common/项目结构总览.md:52,55-56`、两个不同 remote、父库 `ls-files` 为空）可进收口票的「仓库边界」区，避免把 `@OLD/apps/nongyu-backend-plus` 误当成一个待核查的旧代码目录。[来源: @OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,55-56；@NEW/.git/config:8-10；@OLD/.git/config:11-13；本会话 `git -C @OLD ls-files` 输出（无行号）]
5. 与 WebVPN 直接相关的既有边界 `@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20`「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」是仓内一手规格原文，事实底稿已列；本票未对该边界做任何判断。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:72,145,176,203]
6. 仓内 `docs/adr/` 目录不存在的核实结果，若收口票要引用「ADR 先例」可直接写「仓内未找到」。[来源: 本会话 `glob docs/adr/**` 与 `Test-Path` 输出（无行号）；@OLD/docs/agents/domain.md:7,12,23]

---

## 尚未查清

以下均为本次**仓内未找到 / 未核实**，不推测填充：

1. **旧 monorepo 是否曾存在过另一个内容不同的 `apps/nongyu-backend-plus` 目录**：本次只读当前工作树与两套 git 元数据的当前状态，未逐 commit 追溯 `@OLD` 的历史树；`git log` 只看了嵌套库与父库最近几条。若历史上父库曾跟踪过同名目录，仓内当前 `ls-files` 为空，**仓内未找到**相关证据。[来源: 本会话 `git -C @OLD ls-files --stage`、`git -C @OLD log --oneline -5` 输出（无行号）]
2. **嵌套仓库远端（GitHub `nongyu-backend-plus.git`）的服务器端状态**：只读了本地 `.git/config` 与本地 log，未访问网络，远端分支 / 是否推送过 **仓内未找到**。[来源: @NEW/.git/config:8-10；本会话范围说明]
3. **`docs/nongyu-go-track-server/tech/埋点迁移压测报告-full-node.md` 全文**：只确认路径存在，未逐行读取；`docs/nongyu-go-track-server/plans/埋点服务一期.md` 与 3 份后续 Spec 同理只确认路径存在。[来源: 本会话 `Get-ChildItem -Recurse` 输出（无行号）]
4. **该迁移的实际执行时间线 / 机器上真实运行状态**：文档自述状态不一致（见上节），本次未追溯提交历史、未登录机器，**仓内未找到**可判定「哪一份状态最新」的一手证据。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:3；@OLD/docs/nongyu-go-track-server/plans/埋点服务迁移至Node.md:8；@OLD/docs/nongyu-go-track-server/部署与发布.md:199]
5. **`.github/workflows/cd.yml` 全文的 Go 相关残留**：本次只用 grep 抽取了 5 行 Node 语义命中，未逐行通读 250+ 行全文；「未见 Go 交叉编译条目」仅限 grep 结果范围。[来源: 本会话 `grep "deploy-track|apps/nongyu-node-track-server|apps/nongyu-go-track-server" @OLD/.github/workflows/cd.yml` 输出（无行号）]
6. **`@OLD` 与 `@NEW` 的 `.git/logs/` reflog 内容**：只确认目录存在，未读取；两套仓库的创建先后关系 **仓内未找到** 可精确到时间的证据（仅有嵌套库 3 个提交的日期 2026-11-06）。[来源: 本会话 `Get-ChildItem ...\.git -Force` 与 `git -C @NEW log --format=...` 输出（无行号）]

---

## 不适用说明

- 本文**不判断迁移方案**：不含「该不该迁移 WebVPN / 该不该照抄旧迁移先例 / 该怎么设计 Spec」的结论。维护者已拍板「本次只把 WebVPN 能力功能重写进新 Go 后端，产出一份 Spec；其余旧能力 Out of scope」，本票只做事实核查，不占该决策。[来源: 本任务约束]
- **未改除本文件外的任何文件**；未调用任何 `deck_*` 工具、未创建 issue / 分支、未 `git commit`、未推送。全局 `~/.dsh/AGENTS.md` 的「任务完成后 git add / commit」要求与本任务「不要 git commit」冲突，按更具体的任务约束执行，是否提交由父代理 / 维护者决定。[来源: 本任务约束；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:217]
- **未引用二手转述**（博客、问答、外部网页）；所有结论均来自 `@OLD/` 与 `@NEW/` 仓内一手文件（文档、源码、配置、`.gitignore`、`.git/` 元数据）与本会话对目录 / git 元数据的只读命令输出。[来源: 本会话全部工具调用]
- 「同一物理目录」的结论基于 `Resolve-Path` / `git rev-parse --show-toplevel` / 非 junction 检查，均为本地文件系统与 git 的只读观测，未对文件系统做任何写操作。[来源: 本会话 pwsh 与 git 命令输出（无行号）]
- 事实底稿 :123 与 :204 的「不同实体 / 内容未读、需另行核对」表述由本文核对补齐；本文**不编辑底稿文件**，是否修订由父代理 / 维护者决定。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:123,204；本任务约束]
