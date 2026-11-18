package schoolauth_test

// 本文件汇总原拼接文件里**没有来源标记**、且无法确定唯一归属的用例。
//
// 为什么单独放：审计指出原 jiaowu_test.go 里有若干段落直接粘在上一段尾巴上，
// 没有 `// ---- from xxx.go ----` 标记。拆分时按以下口径处理：
//   - 能按主题判断归属的（envelopeDoer → ywtb_test.go，failDoer/mustJiaowuSource → tgt_test.go，
//     SiteProfiles → sso_test.go，XSGZ 三组 → xsgz_*_test.go），已归入对应文件；
//   - 剩下的这两条没有明确主题归属，按指示**不猜**，统一放这里保住它们。
//
// 这些用例在原拼接文件里没有来源标记，按主题暂归此处。

import (
	"context"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestFetchXSGZToken_RejectsEmptyTGT(t *testing.T) {
	d := &scriptedDoer{responses: map[string]*scriptedResp{}}
	if _, err := schoolauth.FetchXSGZToken(context.Background(), d, "", "  "); err == nil {
		t.Fatal("空 tgt 必须被拒绝")
	}
}
