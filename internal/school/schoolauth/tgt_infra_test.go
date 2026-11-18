package schoolauth_test

// #25 任务 4 补强：网络/解析等**基础设施故障**绝不能被误标成「凭据失效」。
// 这是把用户引向「重登」的错误方向——票面要求两类必须可区分。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestTGTManager_RefreshInfraFailureNotMislabelledAsCredentialIssue(t *testing.T) {
	// failDoer 让换票请求直接失败 → 属基础设施（网络）故障，不是学校拒绝
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	m := newManagerWithSource(t, src)

	_, err = m.EnsureFresh(context.Background(), "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("网络故障时续期必须失败")
	}
	// 不得被归为「凭据失效」
	if errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatalf("网络故障不得标成凭据失效（会误导用户去重登），实际 %v", err)
	}
	// 应明确指向基础设施问题
	if !strings.Contains(err.Error(), "基础设施") {
		t.Fatalf("应提示基础设施问题，实际 %q", err.Error())
	}
}
