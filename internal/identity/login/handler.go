package login

// 登录子域**不提供 HTTP 入口**。
//
// 原因：登录成功后必须签发会话，而签发属于 session 子域。
// 若在这里直接暴露 handler，就会出现「登录成功但没票」或「handler 里塞编排」两种坏形态。
//
// 因此 HTTP 入口统一在 internal/identity/flow（编排层），由它先调本包的登录方法、
// 再调 session 子域签发。本包只暴露 Service 的方法。
