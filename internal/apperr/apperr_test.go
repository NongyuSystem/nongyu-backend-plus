package apperr_test

import (
	"errors"
	"testing"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content"
)

// apperr 是全仓唯一的「错误码 → 判定 / HTTP 状态」收口点。
// 这里锁住三件事：别名互通、状态映射一致、非领域错误不伪装成 4xx。

func TestCodeOf_RecognizesWrappedErrors(t *testing.T) {
	// 用 %w 包一层后仍要能取出码——否则 handler 里一旦包装就退化成 500。
	base := apperr.Errorf(apperr.CodeNotFound, "对象不存在")
	wrapped := errors.Join(errors.New("上下文"), base)

	code, ok := apperr.CodeOf(wrapped)
	if !ok || code != apperr.CodeNotFound {
		t.Fatalf("包装后的领域错误应能取到码，得到 %v %v", code, ok)
	}
}

func TestCodeOf_NonDomainErrorIsNotRecognized(t *testing.T) {
	// 普通错误（DB 故障等）不能被认成领域错误——否则会被当成 4xx 返回，
	// 把「我们自己的故障」说成「你请求有问题」。
	if _, ok := apperr.CodeOf(errors.New("connection refused")); ok {
		t.Fatal("非领域错误不该被识别成领域错误")
	}
}

// TestContentAndApperrAreInteroperable 是本轮修掉的那个分裂的回归用例。
//
// 早先 content 与 apperr 各有一套 Code/Error/Errorf/CodeOf，
// content.Errorf 造出的错误 apperr.CodeOf 认不出 → 400 被错报成 500。
// 现在 content.Code 是 apperr.Code 的别名，两条路径必须互通。
func TestContentAndApperrAreInteroperable(t *testing.T) {
	ce := content.Errorf(content.CodeLocked, "超过编辑窗口")
	code, ok := apperr.CodeOf(ce)
	if !ok {
		t.Fatal("content.Errorf 造出的错误必须能被 apperr.CodeOf 识别（否则业务错误会退化成 500）")
	}
	if code != apperr.CodeLocked {
		t.Fatalf("错误码应保持 locked，得到 %v", code)
	}
	if got := apperr.HTTPStatus(code); got != 409 {
		t.Fatalf("locked 应映射 409，得到 %d", got)
	}
}

func TestHTTPStatus_MappingIsStable(t *testing.T) {
	// 这张表是「同一个码在所有域映射成同一个状态」的唯一保证。
	cases := map[apperr.Code]int{
		apperr.CodeUnauthorized:       401,
		apperr.CodeForbidden:          403,
		apperr.CodeNotFound:           404,
		apperr.CodeRecipientNotFound:  404,
		apperr.CodeLocked:             409,
		apperr.CodeAttachmentTooLarge: 413,
		apperr.CodePayloadTooLarge:    413,
		apperr.CodeUnsupportedMedia:   415,
		apperr.CodeRateLimited:        429,
		apperr.CodeValidationFailed:   400,
		apperr.CodeInvalidCursor:      400,
		apperr.CodeTooManyAttachments: 400,
		apperr.CodeSelfSendForbidden:  400,
	}
	for code, want := range cases {
		if got := apperr.HTTPStatus(code); got != want {
			t.Errorf("%s 应映射 %d，实际 %d", code, want, got)
		}
	}
}

func TestHTTPStatus_UnknownCodeIs400(t *testing.T) {
	// 未知码保守归 400（仍是客户端错误），不能归 2xx 或 5xx。
	if got := apperr.HTTPStatus(apperr.Code("something_new")); got != 400 {
		t.Fatalf("未知码应归 400，得到 %d", got)
	}
}
