package schoolauth_test

// 后端自取 token + enterHall 前置（2026-11-10 实测）。
//
// 这两条锁住本轮最关键的发现：
//   ① 后端能用 tgt 自己换出 token（不需要浏览器/WebView）
//   ② 取 detail 前必须先走 enterHall，否则 500「验证权限异常」
// 曾经因为漏掉 ②，我把原因误判成「服务端识别客户端环境」，白查了很久。
//
// 来源说明：本段在原拼接文件里用 `// ── ... ──` 标注，没有 `// ---- from` 标记，
// 按主题归入本文件。

import (
	"context"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestEnterXSGZHall_CallsBothSteps(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/gateway/xg-agg-pc/user/checkUserAndGetUserLevel": {
			status: 200,
			body: `{"code":"0","data":{"loginLevels":[` +
				`{"levelNo":"XS","units":[{"unitType":2,"unitNo":"419"}]}]}}`,
		},
		"/gateway/xg-agg-pc/user/enterHall": {
			status: 200,
			body:   `{"code":"0","data":{}}`,
		},
	}}
	if err := schoolauth.EnterXSGZHall(context.Background(), d, "https://xsgz.sicau.edu.cn", "tok"); err != nil {
		t.Fatalf("EnterXSGZHall: %v", err)
	}
	// 两步都必须发出
	var gotLevel, gotEnter bool
	for _, r := range d.requests {
		switch r.path {
		case "/gateway/xg-agg-pc/user/checkUserAndGetUserLevel":
			gotLevel = true
		case "/gateway/xg-agg-pc/user/enterHall":
			gotEnter = true
		}
	}
	if !gotLevel || !gotEnter {
		t.Fatalf("必须依次调用两个接口，实际 level=%v enterHall=%v", gotLevel, gotEnter)
	}
}

func TestEnterXSGZHall_PropagatesBusinessError(t *testing.T) {
	// 业务码非 0 必须报错，否则调用方会以为升级成功、继续去打必然 500 的 detail。
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/gateway/xg-agg-pc/user/checkUserAndGetUserLevel": {
			status: 200,
			body:   `{"code":"500","message":"验证权限异常"}`,
		},
	}}
	if err := schoolauth.EnterXSGZHall(context.Background(), d, "", "tok"); err == nil {
		t.Fatal("业务码非 0 时 EnterXSGZHall 必须报错")
	}
}
