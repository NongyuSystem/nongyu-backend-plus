// Package bridge 放 internal/app 的**跨域适配器**。
//
// 这些类型做的事都一样：把 A 域要求的接口形状，接成 B 域能用的形状。
// 例如 schoolauth 的 Credential/Identity 与 identity/login 的
// SchoolCredential/SchoolIdentity 是两套类型，中间需要一层翻译。
//
// 为什么要单独成包：装配层（app 根）的职责是「构造 + 注入」，不该同时塞满
// 这些翻译细节。把它们集中在这里，app 根的每个 assemble_*.go 只剩接线，
// 而「跨域翻译到底做了什么」也能在一个目录里读完。
//
// 约定：
//   - 本包只做形状转换与窄接口实现，不含业务判断、不碰 SQL；
//   - 结构体字段一律未导出，外部通过 NewXxx 构造函数创建
//     （这样装配层无法绕过校验直接拼装半成品）。
package bridge
