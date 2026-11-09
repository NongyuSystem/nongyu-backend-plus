package schoolauth

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"nongyu-backend-plus/internal/platform"
)

// maxBodyBytes 是响应体上限：身份/学籍页面都是小页面，超过这个量说明拿到的
// 不是预期内容（可能被重定向到登录页或门户首页），按失败处理，不读进内存。
const maxBodyBytes = 1 << 20 // 1 MiB

// newCookieRequest 构造一个带会话 cookie 的 GET 请求。
func newCookieRequest(ctx context.Context, url, cookieHeader string) (*http.Request, error) {
	if url == "" {
		return nil, fmt.Errorf("schoolauth: 目标地址未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader)
	// 这些页面是 GBK 的 ASP 老系统（见知识库 bjiben.asp 那条），
	// 但这里只取原始字节，解码交给解析函数，避免在传输层做编码假设。
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; nongyu-backend-plus)")
	return req, nil
}

// fetch 发起请求并读回原始字节。
//
// 只接受 200：学校站在会话失效时常常返回 200 + 登录页，所以**不能只看状态码**，
// 还需要解析函数校验内容结构（解析失败即视为失败）。
func fetch(ctx context.Context, doer platform.HTTPDoer, req *http.Request) ([]byte, error) {
	resp, err := doer.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schoolauth: 上游返回 %d", resp.StatusCode)
	}
	// 限制读取量，防止异常超大响应打爆内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("schoolauth: 响应体超过上限 %d", maxBodyBytes)
	}
	return body, nil
}
