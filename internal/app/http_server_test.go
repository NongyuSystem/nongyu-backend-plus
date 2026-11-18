package app

// #21 M7：HTTP 服务器必须有读、写、空闲三个超时。
// 期望值来自票正文验收判据，不是从实现里重算。

import (
	"testing"
	"time"
)

func TestHTTPServerTimeouts_ReadWriteIdleAllSet(t *testing.T) {
	// 直接构造被测的最小对象：NewHTTPServer 是装配层用来产出 *http.Server 的公共 seam。
	srv := NewHTTPServer(":0", nil, 8*time.Second, 20*time.Second, 90*time.Second)
	if srv.ReadTimeout != 8*time.Second {
		t.Fatalf("ReadTimeout 应为 8s，实际 %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 20*time.Second {
		t.Fatalf("WriteTimeout 应为 20s，实际 %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 90*time.Second {
		t.Fatalf("IdleTimeout 应为 90s，实际 %v", srv.IdleTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout 也应保留")
	}
}
