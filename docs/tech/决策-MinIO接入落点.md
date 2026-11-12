# 决议：MinIO 接入落点

> 来源：[决议：MinIO 接入落点（platform 接口、上传方式、key 规范、签名 URL）](https://github.com/NongyuSystem/nongyu-backend-plus/issues/53)（wayfinder map #43 子票），2026-11-12 收口（HITL grilling，Q0/Q1~Q6 全 A）。
> 性质：**决策记录**，不是 Spec、不是实现。选型不重开（map Q6 已锁自建 S3 兼容）。

## 前置（已锁，不在本票重开）

- **选型**：自建 S3 兼容（MinIO），**不对比 OSS／本地盘**（map Q6）。
- **附件上限**（[#49 Q4/Q10](决策-帖子域模型与契约.md):34）：「**9 张 / 单张 5MB / `jpg|png|webp|gif`**」，**属「提案升格为硬约束」**（来源=广场发帖增强 PRD，Spec 须标注，不得写成「沿用既有契约」）。
- **公告本期不做图片**（[#44 Q8](决策-公告域模型与作用域.md)）：两域附件能力**不对称**，由 [#54](https://github.com/NongyuSystem/nongyu-backend-plus/issues/54) 汇总。
- **架构约束**：`README.md:45` 业务域只依赖 `internal/platform` 接口、**不直接 import S3 SDK**；`:46` 进程无状态、状态外置（DB／缓存／**对象存储**）；`:47` 存储实现放 `internal/platform` 或其子包。
- **platform 现状**：13 个 interface（`TicketStore`/`Cipher`/`HTTPDoer`/`Clock`/`Mailer`/`AuditStore`/`Store`/`UserRepo`/`ProfileRepo`/`LoginMethodRepo`/`SessionStore`/`StudentBriefReader`/`LockBackend`），**无对象存储接缝**。
- **`go.mod` 现状**：直接依赖仅 gin／pgx／go-redis／jsonschema／x/text，**无任何 S3 SDK**。

## 决议（Q0/Q1~Q6 全 A）

**Q0/Q1 继承清单 + platform 接口形状**：
- **逐条继承五项**：① 自建 S3 兼容；② 对象 key **按域分前缀**（见 Q4，不照抄单一 `posts/` 前缀）；③ **9 张 / 5MB / 四格式作为硬约束继承**（执行点按 Q2）；④ **不引 OSS／本地盘**；⑤ **预签名 URL**（用于读，见 Q3）。
- **接口**：`internal/platform` 新增 **`ObjectStore`**，方法集**最小化**为：
  - `Put(ctx, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)`
  - `PresignGet(ctx, key string, ttl time.Duration) (string, error)`
  - `Delete(ctx, key string) error`
  - `Stat(ctx, key string) (ObjectInfo, error)`
- **不含 `PresignPut`**（Q2 选后端中转，不需要；将来若要直传再加）。
- **实现放 `internal/platform/objectstore/`** 子包。
- 否决：接口塞 `List`/`Copy`/分片上传（现在没人用，违反「不提前建」）；接口放业务域（与 `README.md:45/47` 相悖）。

**Q2 上传方式 = 后端中转**：客户端 multipart 上传到 Go 服务，服务端校验后 `Put` 到 MinIO。
- 理由：**5MB 与四格式白名单能在后端真正执行**（#49 已定为硬约束）；`MaxBytesReader` 与 `RateLimitMiddleware` 可直接复用；**流式转发不落本地盘**，不破坏「进程无状态」。
- **代价（已记录）**：附件流量过 Go 服务（5MB×9 张 ×并发），占用带宽与内存；大文件场景会成为瓶颈。
- 补偿：**流式读**（不整体入内存）+ 单请求体积硬限 + 复用既有全站限流。
- 否决：客户端预签名直传（5MB 只能靠桶策略/客户端，后端**无法真正执行硬约束**，还要额外做上传票据）；两者都支持（两套路径与校验，本期没必要）。

**Q3 资源访问鉴权 = 预签名 GET + 短 TTL**：
- 由**后端在「读帖子详情」时按当前用户鉴权**（复用 [#46](决策-横切鉴权口径.md) 的会话与可见性规则），**鉴权通过后才签发**该对象的临时 URL；TTL 短（**具体值交 #51**，建议 5~15 分钟）。
- **显式接受的代价**：帖子软删后，**已签发的 URL 在 TTL 内仍可访问**（无法回收）；TTL 越短风险越小。
- 否决：**公开永久 URL**（软删形同虚设、URL 泄漏即永久泄漏、绕过 #49 的匿名性边界）；经后端 302 代理流（图片流量全过服务，与「静态资源外置」取向相反）。

**Q4 对象 key 与桶/生命周期 = 单桶 + 按域分前缀**：
- 帖子附件：`posts/{yyyy}/{MM}/{uuid}.{ext}`（**沿用既有 PRD 形状**——#49 已标其为提案来源，照抄形状风险最低）。
- 预留：`announcements/{yyyy}/{MM}/{uuid}.{ext}`（#44 本期不做图片，但**前缀先定**避免将来迁移）。
- **本期只实现 `posts/`**，其余前缀不落代码。
- **生命周期**：本期**不配自动删除规则**；帖子软删时由**应用层 `Delete`** 清对象，**失败只记日志 + 计数、不阻塞软删**（与通知/审计同源的旁路取向）。
- 否决：按域分桶（桶策略/凭证/监控面翻倍，收益不明确）；单一扁平前缀（将来按域清理/统计困难）。

**Q5 依赖 = `github.com/minio/minio-go/v7`**：
- Apache-2.0、纯 Go、无 CGO、MinIO 官方客户端、原生支持 presigned。
- **依赖隔离**：SDK **只允许 `internal/platform/objectstore/` import**，业务域一律只见 `platform.ObjectStore` 接口 —— Spec 须用 `go list -deps` 断言。
- **满足根 `AGENTS.md`「停用即删」双确认判据**：① `go mod tidy` 后 `go.mod` 不再出现该 `require`；② `go list -deps ./...` 不再含该包路径。**不手改 `go.sum`**。
- **引入理由须写进 commit 说明**。
- 否决：AWS SDK v2（依赖树更大、为兼容存储引入 AWS 配置模型）；手写 SigV4（签名/分片/重试全自己写，风险远大于收益）。

**Q6 「MinIO 行为未验证」= 按 AWS S3 语义定契约 + 列 Spec pending 验收**：
- **按 AWS S3 公开语义定契约**（presigned URL 标准语义、`x-amz-*` 查询参数、TTL 上限）。
- 把「**MinIO 实际行为核对**」列入 [#51](https://github.com/NongyuSystem/nongyu-backend-plus/issues/51) 的 **pending 验收项**。
- **在 Spec 里写明依据是 AWS S3 文档而非 MinIO 实测** —— 不许假装已知。
- 否决：不写依据只写「用预签名 URL」（后来者既不知道来源、也不知道未验证）；现在实测（需启动 MinIO，属 HITL task，可另开票——**本票不阻塞于此**）。

## 本票未裁定 / 留 fog

- **预签名 TTL 具体值**、**单帖附件总大小上限**（9×5MB 是否还要额外总限）→ #51。
- **上传端点的路径**与是否入 `publicReadablePaths` → 归 #46 已定口径，本票只引用。
- **图片是否做服务端二次处理**（压缩/缩略图/EXIF 剥离）：本期倾向不做。
- **桶的部署形态**（单机/分布式、副本数、容量配额）→ #51 或运维，非本票。
- **`Delete` 失败的重试策略** → 依赖 Q4 的旁路取向，交 #51。
- **MinIO 实际行为核对**（Q6）→ Spec pending 验收项。

## 对下游票的约束

- **[#54 架构落点](https://github.com/NongyuSystem/nongyu-backend-plus/issues/54)**：`platform.ObjectStore` 接口 + `platform/objectstore/` 子包；上传走**后端中转**端点（流式 + `MaxBytesReader`）；**业务域不 import S3 SDK**；帖子域只存**对象 key 引用**（不存 URL）；两域附件不对称（公告本期不做）需在兼容性说明里写清。
- **[#51 Spec 收口](https://github.com/NongyuSystem/nongyu-backend-plus/issues/51)**：验收须含 Q0/Q1 接口方法集与放置、Q2 后端中转与体积硬限、Q3 预签名短 TTL + 软删残留窗口、Q4 key 规范与生命周期、Q5 依赖隔离断言（`go list -deps`）、**Q6 的 MinIO 实测 pending 项**；TTL 值、总大小上限、Delete 重试列 pending。

## 证据

`internal/platform/store.go`／`outbound.go`／`mail.go`／`cache.go`（13 个 interface 清单，无对象存储接缝）；`go.mod`:5-11（直接依赖无 S3 SDK）；`README.md`:44-48（架构约束，`:45` 禁直接 import S3 SDK、`:46` 对象存储为外置状态、`:47` 实现放 platform 子包）；[决策-帖子域模型与契约.md](决策-帖子域模型与契约.md):34（附件上限与「提案升格」标注）；[决策-公告域模型与作用域.md](决策-公告域模型与作用域.md)（公告不做图片）；[决策-横切鉴权口径.md](决策-横切鉴权口径.md)（读鉴权口径）；`../../apps/nongyu-app-plus/docs/research/skin-decoupling-routes.md:358`（MinIO API 行为未验证的自标）；根 `AGENTS.md`（依赖「停用即删」双确认判据）；票 #53 的票评与 resolution comment；map #43 的 Decisions so far 对应行。
