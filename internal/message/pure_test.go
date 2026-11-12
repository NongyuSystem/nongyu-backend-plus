package message_test

import (
	"strings"
	"testing"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/message"
)

// 这一层是**纯函数单测（无 IO）**：不启 HTTP、不碰仓储、不注入时钟/限流器，
// 只喂数据给领域里的纯函数，断言外部可观察的返回值与错误码。
// 依据 docs/specs/spec-六类常见功能.md 的 Testing Decisions ①。
//
// 覆盖：幂等键组装、kind 校验（通知不接受外部直接建）、发给自己过滤、
// body 长度上限。

func TestNotificationKeyOf_AssemblesIdempotencyKey(t *testing.T) {
	// 幂 etc 键 = (recipientId, sourceDomain, sourceObjectId)，
	// sourceObjectId 含动作与靶子（like:post:88 / reply:1234 / like:comment:99），
	// 见 docs/tech/决策-通知派生规则.md Q4。
	cases := []struct {
		name string
		in   message.EmitInput
		want message.NotificationKey
	}{
		{
			name: "帖子被点赞",
			in: message.EmitInput{
				RecipientID: 42, ActorID: 7, SourceDomain: "post", SourceObjectID: "like:post:88",
				Title: "有人赞了你的帖子", Body: "用户A 赞了《秋收》",
			},
			want: message.NotificationKey{RecipientID: 42, SourceDomain: "post", SourceObjectID: "like:post:88"},
		},
		{
			name: "评论被回复",
			in: message.EmitInput{
				RecipientID: 9, ActorID: 3, SourceDomain: "post", SourceObjectID: "reply:1234",
			},
			want: message.NotificationKey{RecipientID: 9, SourceDomain: "post", SourceObjectID: "reply:1234"},
		},
		{
			name: "公告发布带 publishRevision",
			in: message.EmitInput{
				RecipientID: 100, ActorID: 0, SourceDomain: "announcement", SourceObjectID: "publish:55:rev3",
			},
			want: message.NotificationKey{RecipientID: 100, SourceDomain: "announcement", SourceObjectID: "publish:55:rev3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := message.NotificationKeyOf(tc.in)
			if got != tc.want {
				t.Fatalf("幂等键组装错误：got=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestNotificationKeyOf_DifferentRecipientsDoNotMerge(t *testing.T) {
	// 同一来源动作发给不同收件人 → 不同的行（per-user 投递行，不能合并成一条）。
	base := message.EmitInput{ActorID: 7, SourceDomain: "post", SourceObjectID: "like:post:88"}
	a := message.NotificationKeyOf(message.EmitInput{RecipientID: 1, SourceDomain: base.SourceDomain, SourceObjectID: base.SourceObjectID})
	b := message.NotificationKeyOf(message.EmitInput{RecipientID: 2, SourceDomain: base.SourceDomain, SourceObjectID: base.SourceObjectID})
	if a == b {
		t.Fatal("不同收件人不应合并为同一幂等键")
	}
}

func TestNotificationKeyString_DelimiterAvoidsCollision(t *testing.T) {
	// sourceObjectId 本身含冒号，若用 ':' 拼接会碰撞；实现用 \x00。
	k1 := message.NotificationKey{RecipientID: 1, SourceDomain: "post", SourceObjectID: "like:post:88"}
	k2 := message.NotificationKey{RecipientID: 1, SourceDomain: "post:like", SourceObjectID: "post:88"}
	if k1.String() == k2.String() {
		t.Fatal("不同四元组不应编成同一个索引串")
	}
	if !strings.Contains(k1.String(), "\x00") {
		t.Fatal("索引串应使用不可见分隔符，避免与含冒号的 sourceObjectId 碰撞")
	}
}

func TestParseMailKind_OnlyInternalMailAllowedFromOutside(t *testing.T) {
	// Spec：POST /messages 仅 internal_mail；通知不接受外部直接建。
	cases := []struct {
		raw     string
		want    message.Kind
		wantErr bool
	}{
		{"", message.KindInternalMail, false},              // 空串默认内部信
		{"internal_mail", message.KindInternalMail, false}, // 显式内部信
		{"notification", "", true},                         // 通知必须拒
		{"nope", "", true},                                 // 未知取值必须拒
		{"INTERNAL_MAIL", "", true},                        // 大小写不放宽
	}
	for _, tc := range cases {
		t.Run("kind="+tc.raw, func(t *testing.T) {
			got, err := message.ParseMailKind(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseMailKind(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ParseMailKind(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if tc.wantErr {
				if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeValidationFailed {
					t.Fatalf("拒绝时应返回 validation_failed，得到 %v", err)
				}
			}
		})
	}
}

func TestParseKind_EnumClosedSet(t *testing.T) {
	for _, raw := range []string{"notification", "internal_mail"} {
		if _, ok := message.ParseKind(raw); !ok {
			t.Fatalf("合法 kind %q 应通过", raw)
		}
	}
	for _, raw := range []string{"", "NOTIFICATION", "mail", "system"} {
		if _, ok := message.ParseKind(raw); ok {
			t.Fatalf("非法 kind %q 应被拒（fail-closed）", raw)
		}
	}
}

func TestIsSelfEmit_ModelLayerFilter(t *testing.T) {
	// 决策-消息域模型.md D2 + 决策-通知派生规则.md Q5：
	// 模型层按 actorId==recipientId fail-closed 拒一次（编排层再拒一次，双保险）。
	cases := []struct {
		name string
		in   message.EmitInput
		want bool
	}{
		{"自赞自收", message.EmitInput{RecipientID: 7, ActorID: 7}, true},
		{"他人触发", message.EmitInput{RecipientID: 7, ActorID: 8}, false},
		{"系统触发不误杀", message.EmitInput{RecipientID: 7, ActorID: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := message.IsSelfEmit(tc.in); got != tc.want {
				t.Fatalf("IsSelfEmit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateMail_BodyAndTitleLimits(t *testing.T) {
	valid := message.CreateMailInput{SenderID: 1, RecipientID: 2, Title: "你好", Body: "正文"}
	if err := message.ValidateMail(valid); err != nil {
		t.Fatalf("合法内部信应通过：%v", err)
	}

	cases := []struct {
		name string
		in   message.CreateMailInput
		code apperr.Code
	}{
		{"空标题", message.CreateMailInput{SenderID: 1, RecipientID: 2, Body: "b"}, apperr.CodeValidationFailed},
		{"标题超长", message.CreateMailInput{SenderID: 1, RecipientID: 2, Title: strings.Repeat("x", message.MaxTitleLen+1), Body: "b"}, apperr.CodeValidationFailed},
		{"空正文", message.CreateMailInput{SenderID: 1, RecipientID: 2, Title: "t"}, apperr.CodeValidationFailed},
		{"正文超长", message.CreateMailInput{SenderID: 1, RecipientID: 2, Title: "t", Body: strings.Repeat("x", message.MaxBodyLen+1)}, apperr.CodeValidationFailed},
		{"发给自己", message.CreateMailInput{SenderID: 1, RecipientID: 1, Title: "t", Body: "b"}, apperr.CodeSelfSendForbidden},
		{"收件人 id 非法", message.CreateMailInput{SenderID: 1, RecipientID: 0, Title: "t", Body: "b"}, apperr.CodeValidationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := message.ValidateMail(tc.in)
			if err == nil {
				t.Fatal("期望被拒，但通过了")
			}
			code, ok := apperr.CodeOf(err)
			if !ok {
				t.Fatalf("应返回带码的领域错误，得到 %v", err)
			}
			if code != tc.code {
				t.Fatalf("错误码 = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestValidateMail_BoundaryExactLimitPasses(t *testing.T) {
	// 上限是「≤ Max*Len 允许」：正好等于上限的正文/标题必须通过。
	in := message.CreateMailInput{
		SenderID: 1, RecipientID: 2,
		Title: strings.Repeat("x", message.MaxTitleLen),
		Body:  strings.Repeat("x", message.MaxBodyLen),
	}
	if err := message.ValidateMail(in); err != nil {
		t.Fatalf("恰好等于上限应通过：%v", err)
	}
}

func TestMessageUnread_SoftDeletedNotCounted(t *testing.T) {
	// 未读数口径：count(readAt is null and deletedAt is null)，软删优先。
	m := &message.Message{}
	if !m.Unread() {
		t.Fatal("新消息默认应未读")
	}
	deleted := &message.Message{DeletedAt: nowPtr()}
	if deleted.Unread() {
		t.Fatal("已软删的未读消息不应计入未读")
	}
	read := &message.Message{ReadAt: nowPtr()}
	if read.Unread() {
		t.Fatal("已读消息不应计入未读")
	}
}
