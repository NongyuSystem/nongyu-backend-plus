package session

// 会话子域**不提供 HTTP 入口**。
//
// 登出（吊销当前会话）需要知道「当前是谁、用的哪个会话」，这属于请求编排，
// 统一放在 internal/identity/flow。签发同理：不通过 HTTP 暴露，
// 避免出现「凭 userID 直接要一张票」的越权入口。
//
// 本包只暴露 Service 的方法（Issue / Authenticate / Revoke / RevokeAll / RequireRole）。
