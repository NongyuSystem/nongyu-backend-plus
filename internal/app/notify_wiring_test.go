package app

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/app/notify"
	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// 本文件锁住「通知派生真的接上了」这件事。
//
// 背景：notifier / message.Emit / 编排层都写好了，但 5 个触发点曾一个都没接，
// 整套通知能力是死代码。纯单测（各域自己的测试）发现不了这种缺口——
// 它需要「跨域」的视角，所以测在装配层。

type nopClock struct{ now time.Time }

func (c nopClock) Now() time.Time { return c.now }

func newNotifyEnv(t *testing.T) (*post.Service, *message.Service, *notify.Notifier, *pgstore.MessageMemoryRepo) {
	t.Helper()
	clock := nopClock{now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}

	msgRepo := pgstore.NewMessageMemoryRepo(clock.Now)
	msgSvc := message.New(message.Options{
		Repo:  msgRepo,
		Clock: clock,
		// 收件人一律视为存在：本测试只关心派生链路，不关心存在性校验。
		UserExists: func(context.Context, int64) (bool, error) { return true, nil },
	})

	notifier := notify.New(notify.Options{
		Sink:     notifySink{svc: msgSvc},
		Capacity: 64,
	})

	postSvc := post.New(post.Options{
		Repo:  pgstore.NewPostMemoryRepo(clock.Now),
		Clock: clock,
	})
	postSvc.SetNotifyHook(postNotifyAdapter{posts: postSvc, notify: notifier}.Handle)

	return postSvc, msgSvc, notifier, msgRepo
}

// TestNotify_PostReplyNotifiesAuthor 覆盖触发点 1：帖子被回复 → 通知帖主。
func TestNotify_PostReplyNotifiesAuthor(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	// 用户 7 发帖，用户 9 回复。
	p, err := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "求助", Body: "正文"})
	if err != nil {
		t.Fatalf("发帖失败：%v", err)
	}
	if _, err := postSvc.AddReply(ctx, 9, p.ID, "我来答"); err != nil {
		t.Fatalf("回复失败：%v", err)
	}
	notifier.Flush()

	rows, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	if len(rows) != 1 {
		t.Fatalf("帖主应收到 1 条通知，实际 %d 条", len(rows))
	}
	if rows[0].SourceDomain != "post" {
		t.Fatalf("来源域应为 post，得到 %q", rows[0].SourceDomain)
	}
	if rows[0].Kind != message.KindNotification {
		t.Fatalf("kind 应为通知，得到 %q", rows[0].Kind)
	}
}

// TestNotify_SelfReplyProducesNothing 覆盖 Q5：自己回复自己不产生通知。
func TestNotify_SelfReplyProducesNothing(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	p, _ := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	// 作者自己回复自己
	if _, err := postSvc.AddReply(ctx, 7, p.ID, "自己回自己"); err != nil {
		t.Fatalf("回复失败：%v", err)
	}
	notifier.Flush()

	rows, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	if len(rows) != 0 {
		t.Fatalf("自己回复自己不该产生通知，实际 %d 条", len(rows))
	}
}

// TestNotify_PostLikesMergeIntoSingleRow 覆盖 Q4：同一帖被不同人点赞合并成一行。
func TestNotify_PostLikesMergeIntoSingleRow(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	p, _ := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	// 三个人分别点赞
	for _, uid := range []int64{9, 10, 11} {
		if err := postSvc.React(ctx, uid, "post", p.ID, "like", true); err != nil {
			t.Fatalf("点赞失败：%v", err)
		}
	}
	notifier.Flush()

	rows, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	if len(rows) != 1 {
		t.Fatalf("同帖被多人点赞应合并成 1 行，实际 %d 行", len(rows))
	}
	if rows[0].Hits != 3 {
		t.Fatalf("hits 应累加到 3，得到 %d", rows[0].Hits)
	}
}

// TestNotify_DuplicateLikeDoesNotRefire 覆盖幂等：重复点赞不再打扰帖主。
func TestNotify_DuplicateLikeDoesNotRefire(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	p, _ := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	// 同一用户点两次（第二次是幂等 no-op）
	_ = postSvc.React(ctx, 9, "post", p.ID, "like", true)
	_ = postSvc.React(ctx, 9, "post", p.ID, "like", true)
	notifier.Flush()

	rows, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	if len(rows) != 1 {
		t.Fatalf("应只有 1 行，实际 %d 行", len(rows))
	}
	if rows[0].Hits != 1 {
		t.Fatalf("重复点赞不该累加 hits（repo 返回 created=false 时不触发），得到 %d", rows[0].Hits)
	}
}

// TestNotify_FavoriteDoesNotNotify 覆盖 Q1：收藏不在 5 个触发点内。
func TestNotify_FavoriteDoesNotNotify(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	p, _ := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	if err := postSvc.React(ctx, 9, "post", p.ID, "favorite", true); err != nil {
		t.Fatalf("收藏失败：%v", err)
	}
	notifier.Flush()

	rows, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	if len(rows) != 0 {
		t.Fatalf("收藏不该产生通知，实际 %d 条", len(rows))
	}
}

// TestNotify_CommentLikeNotifiesReplyAuthor 覆盖触发点 3：留言被点赞 → 通知留言作者。
func TestNotify_CommentLikeNotifiesReplyAuthor(t *testing.T) {
	postSvc, _, notifier, msgRepo := newNotifyEnv(t)
	ctx := context.Background()

	p, _ := postSvc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	r, _ := postSvc.AddReply(ctx, 9, p.ID, "我的留言") // 留言作者是 9

	// 用户 11 给留言点赞 → 应通知 9（不是帖主 7）
	if err := postSvc.React(ctx, 11, "comment", r.ID, "like", true); err != nil {
		t.Fatalf("留言点赞失败：%v", err)
	}
	notifier.Flush()

	// 留言作者 9 收到
	rows9, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 9}, message.PageCursor{}, 10)
	if len(rows9) != 1 {
		t.Fatalf("留言作者应收到 1 条通知，实际 %d 条", len(rows9))
	}
	// 帖主 7 不该收到（他只收到了「被回复」那条）
	rows7, _ := msgRepo.List(ctx, message.ListFilter{RecipientID: 7}, message.PageCursor{}, 10)
	for _, r := range rows7 {
		if r.SourceObjectID == "like:comment:"+itoa(r.ID) {
			t.Fatal("留言点赞不该通知帖主")
		}
	}
}

// TestNotify_DisabledHookIsSafe 保证未装钩子时不 panic。
func TestNotify_DisabledHookIsSafe(t *testing.T) {
	clock := nopClock{now: time.Now()}
	svc := post.New(post.Options{Repo: pgstore.NewPostMemoryRepo(clock.Now), Clock: clock})
	ctx := context.Background()
	// 没有 SetNotifyHook：发帖与回复都应正常，不 panic
	p, err := svc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("无钩子时发帖应成功：%v", err)
	}
	if _, err := svc.AddReply(ctx, 9, p.ID, "x"); err != nil {
		t.Fatalf("无钩子时回复应成功：%v", err)
	}
	_ = platform.NewMemoryObjectStore // 保持 import 使用
}

// itoa 是测试内的小工具。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
