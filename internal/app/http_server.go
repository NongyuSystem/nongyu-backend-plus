package app

// #21 M7：HTTP 服务器的超时配置。
//
// 之前 *http.Server 只有 ReadHeaderTimeout，慢客户端可以长期占住连接、
// 慢响应也会一直挂着。补 ReadTimeout / WriteTimeout / IdleTimeout 三个超时，
// 让连接生命周期有明确上界。

import (
	"net/http"
	"time"
)

// 三个超时的默认值（config 未提供时使用）。
const (
	defaultReadTimeout   = 15 * time.Second
	defaultWriteTimeout  = 30 * time.Second
	defaultIdleTimeout   = 90 * time.Second
	defaultHeaderTimeout = 5 * time.Second
)

// NewHTTPServer 构造带完整超时的 HTTP 服务器。
//
// 这是装配层与测试共用的公共 seam：app.New 用它，测试也直接通过它观察
// 三个超时是否生效，而不需要真的起服务或触碰 App 的私有字段。
func NewHTTPServer(addr string, handler http.Handler, read, write, idle time.Duration) *http.Server {
	if read <= 0 {
		read = defaultReadTimeout
	}
	if write <= 0 {
		write = defaultWriteTimeout
	}
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: defaultHeaderTimeout,
		ReadTimeout:       read,
		WriteTimeout:      write,
		IdleTimeout:       idle,
	}
}
