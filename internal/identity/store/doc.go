// Package store 是身份域的持久化实现（用户 / 画像 / 登录方式 / 会话 + 学工档案落库）。
//
// 为什么在这里，而不在 internal/platform：
//   - 仓储实现属于「域」——它实现的是 identity 域定义的 UserRepo / ProfileRepo 等接口，
//     读写的也是这个域的表。按本仓「域内固定分层」的约定，它该在 <域>/store/。
//   - 曾经它放在 platform/pg（那时候叫 PGIdentityStore），是唯一没有归位的域仓储。
//     11-19 把它搬到这里，并把 platform/pg 里依赖它的测试基建改成直接用 *pgxpool.Pool。
//
// 为什么不做兼容别名：
//   - 若在 platform 根加 `type PGIdentityStore = identitystore.PGIdentityStore`，
//     platform 就会 import 业务域，重新引入分层倒置——
//     `internal/architecture` 的 TestPlatformNeverImportsBusinessDomains 明确禁止。
//   - 因此调用方直接用 identitystore.NewPGIdentityStore(...)，没有中间层。
//
// 包名统一叫 store，与其它 6 个域的 store/ 子包一致；调用方按域名起别名
// （identitystore / poststore …）以区分。
package store
