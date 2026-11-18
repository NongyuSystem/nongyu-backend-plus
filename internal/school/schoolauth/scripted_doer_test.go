package schoolauth_test

// scriptedDoer 是按 URL 路径返回预设响应的 HTTP 测试替身，并记录收到的每个请求。
//
// 为什么放在这里：它是整个 schoolauth 测试套件里被引用最广的替身——
// 拆除前的审计显示它横跨 6 个原文件（jiaowu、ywtb、tgt_refresh、tgt_classify、
// xsgz_detail、xsgz_hall、misc_untagged 等），因此必须独立成文件，
// 否则任何一个业务测试文件都会成为其他文件的隐式依赖。

import (
	"context"
	"net/http"
)

// scriptedDoer 按 URL 路径返回预设响应，并记录请求。
type scriptedDoer struct {
	// responses 键是路径（不含 host），值为响应。
	responses map[string]*scriptedResp
	// requests 记录收到的请求（顺序）。
	requests []*recordedReq
}

type scriptedResp struct {
	status   int
	body     string
	location string
	headers  map[string]string
}

type recordedReq struct {
	method  string
	host    string
	path    string
	rawURL  string
	cookie  string
	authHdr string
}

func (d *scriptedDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, &recordedReq{
		method:  req.Method,
		host:    req.URL.Hostname(),
		path:    req.URL.Path,
		rawURL:  req.URL.String(),
		cookie:  req.Header.Get("Cookie"),
		authHdr: req.Header.Get("authorization"),
	})
	// 先按完整 URL 找，再按路径找。
	if r, ok := d.responses[req.URL.String()]; ok {
		return buildResp(r), nil
	}
	if r, ok := d.responses[req.URL.Path]; ok {
		return buildResp(r), nil
	}
	return buildResp(&scriptedResp{status: 404, body: "not found"}), nil
}

func buildResp(r *scriptedResp) *http.Response {
	h := http.Header{}
	for k, v := range r.headers {
		h.Set(k, v)
	}
	if r.location != "" {
		h.Set("Location", r.location)
	}
	status := r.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       ioNopCloser(r.body),
	}
}
