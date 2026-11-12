package pgstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// newMessageRepo 返回一个已迁移好、指向独立 schema 的 PG 消息仓储，
// 以及同一个 schema 的连接池（「库里到底几行」这类独立证据要用它，
// 因为仓储自身的接口不提供「按幂等键数行数」的能力——那不该进领域接口）。
func newMessageRepo(t *testing.T) (*pgstore.MessagePGRepo, *pgxpool.Pool) {
	t.Helper()
	pool := newPGTestPool(t)
	if pool == nil {
		return nil, nil // 已 Skip
	}
	return pgstore.NewMessagePGRepo(pool, fixedNow), pool
}

// msgKey 造一个通知幂等键（服务层算好传入的那种）。
func msgKey(recipient int64, domain, object string) message.NotificationKey {
	return message.NotificationKey{RecipientID: recipient, SourceDomain: domain, SourceObjectID: object}
}

// ── 核心：通知 upsert 合并 ────────────────────────────────────────

// TestMessagePGRepo_UpsertNotificationMerges 是本任务最核心的验收：
// 同一个幂等键重复触发，必须**合并成一行**，且四条语义逐条成立：
//
//	① hits 累加 1（不是覆盖成 1）；
//	② read_at 重置为 NULL（已读的老通知要变回未读）；
//	③ created_at 保持首次写入（合并不会把老通知顶到列表最前）；
//	④ title/body 刷新为最新快照。
//
// 「不产生第二行」用两处独立证据交叉验证：upsert 返回的 ID 不变 +
// 表里 count(*) 恒为 1。只看 ID 不变会漏掉「唯一索引失效但恰好命中同一行」
// 这种假象，而只看 count(*) 又看不出到底合并进了哪一行。
func TestMessagePGRepo_UpsertNotificationMerges(t *testing.T) {
	repo, pool := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(101)
	key := msgKey(recipient, "post", "like:post:88")

	first := fixedNow()
	m1, err := repo.UpsertNotification(ctx, key, "张三赞了你的帖子", "帖子：周末球赛", first)
	if err != nil {
		t.Fatalf("首次 UpsertNotification: %v", err)
	}
	if m1.ID <= 0 {
		t.Fatalf("应回填 ID，实际 %d", m1.ID)
	}
	if m1.Hits != 1 {
		t.Fatalf("首次写入 hits 应为 1，实际 %d", m1.Hits)
	}
	if m1.ReadAt != nil {
		t.Fatalf("首次写入 read_at 应为 NULL，实际 %v", m1.ReadAt)
	}
	if m1.Kind != message.KindNotification {
		t.Fatalf("kind 应为 notification，实际 %q", m1.Kind)
	}
	if m1.SourceDomain != "post" || m1.SourceObjectID != "like:post:88" {
		t.Fatalf("来源两列往返失真：%q / %q", m1.SourceDomain, m1.SourceObjectID)
	}
	if !m1.CreatedAt.Equal(first) {
		t.Fatalf("首次 createdAt 应等于传入的 at：want %v got %v", first, m1.CreatedAt)
	}

	// 先把它读成已读，用来验证「合并会把 read_at 重置回未读」。
	if err := repo.MarkRead(ctx, m1.ID, fixedNow().Add(time.Minute)); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if read, err := repo.FindByID(ctx, m1.ID); err != nil || read.ReadAt == nil {
		t.Fatalf("MarkRead 未生效：err=%v readAt=%v", err, read.ReadAt)
	}

	// 第二次同键触发：时间往后挪，用来证明 created_at 不被覆盖。
	second := first.Add(2 * time.Hour)
	m2, err := repo.UpsertNotification(ctx, key, "李四也赞了你的帖子", "帖子：周末球赛（新快照）", second)
	if err != nil {
		t.Fatalf("第二次 UpsertNotification: %v", err)
	}

	if m2.ID != m1.ID {
		t.Fatalf("同键合并应命中同一行：firstID=%d secondID=%d", m1.ID, m2.ID)
	}
	if m2.Hits != 2 {
		t.Fatalf("hits 应累加为 2，实际 %d", m2.Hits)
	}
	if m2.ReadAt != nil {
		t.Fatalf("合并必须把 read_at 重置为 NULL（变回未读），实际 %v", *m2.ReadAt)
	}
	if !m2.CreatedAt.Equal(first) {
		t.Fatalf("合并不得改 createdAt：want %v got %v", first, m2.CreatedAt)
	}
	if m2.Title != "李四也赞了你的帖子" || m2.Body != "帖子：周末球赛（新快照）" {
		t.Fatalf("title/body 应刷新为最新快照：%q / %q", m2.Title, m2.Body)
	}

	// 第三次：确认 hits 继续累加（不是「第二次特例」）。
	m3, err := repo.UpsertNotification(ctx, key, "王五也赞了你的帖子", "帖子：周末球赛", second.Add(time.Hour))
	if err != nil {
		t.Fatalf("第三次 UpsertNotification: %v", err)
	}
	if m3.Hits != 3 || m3.ID != m1.ID {
		t.Fatalf("第三次应合并进同一行且 hits=3，实际 id=%d hits=%d", m3.ID, m3.Hits)
	}

	// 独立证据：表里只有一行。
	if n := countRows(t, pool, `SELECT count(*) FROM messages WHERE recipient_id = $1`, recipient); n != 1 {
		t.Fatalf("同键通知在库里应有且仅有 1 行，实际 %d 行", n)
	}
}

// ── 幂等键的三个维度都必须参与唯一性 ──────────────────────────────

// TestMessagePGRepo_NotificationKeyDimensions 覆盖：幂等键是
// (recipient, domain, object) 三元组，任一维度不同就是**另一行**。
//
// 这条常被写错成「只按 object 判重」，症状是「同一帖子被两个不同的人赞，
// 收件人只收到一条通知、hits 却是 2」。
func TestMessagePGRepo_NotificationKeyDimensions(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	at := fixedNow()

	base, err := repo.UpsertNotification(ctx, msgKey(1, "post", "like:post:1"), "a", "b", at)
	if err != nil {
		t.Fatalf("基准通知: %v", err)
	}
	// 换 recipient → 新行。
	otherRecipient, err := repo.UpsertNotification(ctx, msgKey(2, "post", "like:post:1"), "a", "b", at)
	if err != nil {
		t.Fatalf("换收件人: %v", err)
	}
	if otherRecipient.ID == base.ID {
		t.Fatal("不同 recipient 应是不同行")
	}
	// 换 domain → 新行。
	otherDomain, err := repo.UpsertNotification(ctx, msgKey(1, "comment", "like:post:1"), "a", "b", at)
	if err != nil {
		t.Fatalf("换域: %v", err)
	}
	if otherDomain.ID == base.ID {
		t.Fatal("不同 source_domain 应是不同行")
	}
	// 换 object → 新行。
	otherObject, err := repo.UpsertNotification(ctx, msgKey(1, "post", "like:post:2"), "a", "b", at)
	if err != nil {
		t.Fatalf("换对象: %v", err)
	}
	if otherObject.ID == base.ID {
		t.Fatal("不同 source_object_id 应是不同行")
	}

	// 同键再来一次 → 仍合并回基准行。
	again, err := repo.UpsertNotification(ctx, msgKey(1, "post", "like:post:1"), "a2", "b2", at.Add(time.Minute))
	if err != nil {
		t.Fatalf("同键再触发: %v", err)
	}
	if again.ID != base.ID || again.Hits != 2 {
		t.Fatalf("同键应合并回基准行且 hits=2：id=%d hits=%d", again.ID, again.Hits)
	}
}

// TestMessagePGRepo_InternalMailNotCollapsed 是部分唯一索引的关键边界：
// uq_messages_notification_source 只对 kind='notification' 生效，
// 内部信的 source 两列为空串，**不能**被一并纳入唯一索引
// （否则同一收件人的所有内部信会互相冲突、只剩一条）。
func TestMessagePGRepo_InternalMailNotCollapsed(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(300)

	// 连发 3 封内部信：source 全为空串。
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		m := &message.Message{
			RecipientID: recipient,
			Kind:        message.KindInternalMail,
			Title:       fmt.Sprintf("内部信 %d", i),
			Body:        "正文",
			Hits:        1,
			SenderID:    int64(400 + i),
			CreatedAt:   fixedNow().Add(time.Duration(i) * time.Minute),
		}
		if err := repo.Create(ctx, m); err != nil {
			t.Fatalf("Create 内部信 #%d: %v", i, err)
		}
		ids = append(ids, m.ID)
	}
	if ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("内部信不应互相合并：%v", ids)
	}

	list, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 封内部信，实际 %d", len(list))
	}
	// sender_id 必须落库（实名可见），NULL 与 0 不能把发送者吞掉。
	if list[0].SenderID == 0 {
		t.Fatal("内部信的 sender_id 应落库，实际为 0")
	}
}

// ── per-user 隔离 / 未读数 / 批量已读 / 非本人 ────────────────────

// TestMessagePGRepo_ListPerUserIsolation 覆盖「只返回本人」：
// 甲的消息绝不出现在乙的列表里，未读数也互不影响。
func TestMessagePGRepo_ListPerUserIsolation(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()

	// 甲 2 条通知、乙 1 条通知。
	if _, err := repo.UpsertNotification(ctx, msgKey(11, "post", "like:1"), "甲1", "b", fixedNow()); err != nil {
		t.Fatalf("甲1: %v", err)
	}
	if _, err := repo.UpsertNotification(ctx, msgKey(11, "post", "reply:1"), "甲2", "b", fixedNow().Add(time.Minute)); err != nil {
		t.Fatalf("甲2: %v", err)
	}
	if _, err := repo.UpsertNotification(ctx, msgKey(22, "post", "like:1"), "乙1", "b", fixedNow().Add(2*time.Minute)); err != nil {
		t.Fatalf("乙1: %v", err)
	}

	listA, err := repo.List(ctx, message.ListFilter{RecipientID: 11}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List 甲: %v", err)
	}
	if len(listA) != 2 {
		t.Fatalf("甲应有 2 条，实际 %d", len(listA))
	}
	for _, m := range listA {
		if m.RecipientID != 11 {
			t.Fatalf("甲的列表里混入了 recipient=%d 的消息", m.RecipientID)
		}
	}

	listB, err := repo.List(ctx, message.ListFilter{RecipientID: 22}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List 乙: %v", err)
	}
	if len(listB) != 1 || listB[0].Title != "乙1" {
		t.Fatalf("乙应只有自己那条，实际 %d 条", len(listB))
	}

	// 未读数：各自独立。
	if n, err := repo.UnreadCount(ctx, message.ListFilter{RecipientID: 11}); err != nil || n != 2 {
		t.Fatalf("甲未读应为 2：n=%d err=%v", n, err)
	}
	if n, err := repo.UnreadCount(ctx, message.ListFilter{RecipientID: 22}); err != nil || n != 1 {
		t.Fatalf("乙未读应为 1：n=%d err=%v", n, err)
	}

	// 乙把乙1 读掉，只影响乙。
	if err := repo.MarkRead(ctx, listB[0].ID, fixedNow()); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: 22}); n != 0 {
		t.Fatalf("乙读完后未读应为 0，实际 %d", n)
	}
	if n, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: 11}); n != 2 {
		t.Fatalf("乙的已读不应影响甲，甲未读仍应为 2，实际 %d", n)
	}
}

// TestMessagePGRepo_UnreadCountAndMarkAllRead 覆盖：未读数只数未读未删；
// 批量已读按 kind 过滤生效、UnreadOnly 开关不改变结果集、软删的不计未读。
func TestMessagePGRepo_UnreadCountAndMarkAllRead(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(77)

	// 2 条通知 + 1 封内部信 + 1 条待软删的通知。
	n1, err := repo.UpsertNotification(ctx, msgKey(recipient, "post", "like:1"), "通知1", "b", fixedNow())
	if err != nil {
		t.Fatalf("通知1: %v", err)
	}
	if _, err := repo.UpsertNotification(ctx, msgKey(recipient, "post", "like:2"), "通知2", "b", fixedNow().Add(time.Minute)); err != nil {
		t.Fatalf("通知2: %v", err)
	}
	mail := &message.Message{
		RecipientID: recipient, Kind: message.KindInternalMail,
		Title: "信", Body: "b", Hits: 1, SenderID: 88, CreatedAt: fixedNow().Add(2 * time.Minute),
	}
	if err := repo.Create(ctx, mail); err != nil {
		t.Fatalf("内部信: %v", err)
	}
	doomed, err := repo.UpsertNotification(ctx, msgKey(recipient, "post", "like:3"), "待删通知", "b", fixedNow().Add(3*time.Minute))
	if err != nil {
		t.Fatalf("待删通知: %v", err)
	}

	// 全量未读：4。
	if n, err := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient}); err != nil || n != 4 {
		t.Fatalf("全量未读应为 4：n=%d err=%v", n, err)
	}
	// 按 kind 过滤：通知 3。
	if n, err := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient, Kind: message.KindNotification}); err != nil || n != 3 {
		t.Fatalf("通知未读应为 3：n=%d err=%v", n, err)
	}
	// 内部信 1。
	if n, err := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient, Kind: message.KindInternalMail}); err != nil || n != 1 {
		t.Fatalf("内部信未读应为 1：n=%d err=%v", n, err)
	}

	// 软删一条：不再计未读。
	if err := repo.MarkDeleted(ctx, doomed.ID, fixedNow()); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if n, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient}); n != 3 {
		t.Fatalf("软删后全量未读应为 3，实际 %d", n)
	}
	// 已软删的也不出现在列表里。
	list, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range list {
		if m.ID == doomed.ID {
			t.Fatal("已软删的消息不应出现在列表里")
		}
	}
	if len(list) != 3 {
		t.Fatalf("列表应剩 3 条，实际 %d", len(list))
	}

	// 批量已读：只作用于 kind=notification 的未读未删行 → 2 条。
	n, err := repo.MarkAllRead(ctx, message.ListFilter{RecipientID: recipient, Kind: message.KindNotification}, fixedNow())
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if n != 2 {
		t.Fatalf("按通知批量已读应影响 2 行，实际 %d", n)
	}
	// 内部信仍未读。
	if c, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient}); c != 1 {
		t.Fatalf("批量已读后应剩内部信 1 条未读，实际 %d", c)
	}
	// 重复批量已读：0 行受影响（幂等）。
	n2, err := repo.MarkAllRead(ctx, message.ListFilter{RecipientID: recipient, Kind: message.KindNotification}, fixedNow())
	if err != nil {
		t.Fatalf("重复 MarkAllRead: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("重复批量已读应影响 0 行，实际 %d", n2)
	}

	// UnreadOnly 开关不改变批量已读的结果集：把剩下的内部信也读掉。
	n3, err := repo.MarkAllRead(ctx, message.ListFilter{RecipientID: recipient, UnreadOnly: true}, fixedNow())
	if err != nil {
		t.Fatalf("带 UnreadOnly 的 MarkAllRead: %v", err)
	}
	if n3 != 1 {
		t.Fatalf("带 UnreadOnly 的批量已读应影响 1 行，实际 %d", n3)
	}
	if c, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient}); c != 0 {
		t.Fatalf("全部读完后未读应为 0，实际 %d", c)
	}

	_ = n1
}

// TestMessagePGRepo_FindByIDAndMarkReadErrors 覆盖 repo 层的错误语义与幂等性：
//   - 不存在 → apperr.CodeNotFound；
//   - 已软删的行 FindByID **仍返回**（归属判定在服务层，repo 不做 owner 过滤）；
//   - 已读再读、已删再删都幂等成功且不覆盖原时间戳。
func TestMessagePGRepo_FindByIDAndMarkReadErrors(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()

	if _, err := repo.FindByID(ctx, 123456); err == nil {
		t.Fatal("不存在的 id 应返回错误")
	} else if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeNotFound {
		t.Fatalf("错误码应为 not_found，实际 %v（%v）", code, err)
	}
	if err := repo.MarkRead(ctx, 123456, fixedNow()); err == nil {
		t.Fatal("对不存在的 id MarkRead 应返回错误")
	} else if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeNotFound {
		t.Fatalf("MarkRead 错误码应为 not_found，实际 %v", code)
	}
	if err := repo.MarkDeleted(ctx, 123456, fixedNow()); err == nil {
		t.Fatal("对不存在的 id MarkDeleted 应返回错误")
	} else if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeNotFound {
		t.Fatalf("MarkDeleted 错误码应为 not_found，实际 %v", code)
	}

	m := &message.Message{
		RecipientID: 55, Kind: message.KindInternalMail,
		Title: "t", Body: "b", Hits: 1, SenderID: 66, CreatedAt: fixedNow(),
	}
	if err := repo.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	readAt := fixedNow().Add(10 * time.Minute)
	if err := repo.MarkRead(ctx, m.ID, readAt); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	// 已读再读：幂等成功，且**不覆盖**第一次的时间戳。
	if err := repo.MarkRead(ctx, m.ID, readAt.Add(time.Hour)); err != nil {
		t.Fatalf("重复 MarkRead 应幂等成功：%v", err)
	}
	got, err := repo.FindByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ReadAt == nil || !got.ReadAt.Equal(readAt) {
		t.Fatalf("重复 MarkRead 不应改第一次的 read_at：want %v got %v", readAt, got.ReadAt)
	}

	// 软删后 FindByID 仍返回完整行（服务层据此判「非本人/已删」）。
	deletedAt := fixedNow().Add(2 * time.Hour)
	if err := repo.MarkDeleted(ctx, m.ID, deletedAt); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	gone, err := repo.FindByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("软删后 FindByID 仍应返回行（归属判定在服务层）：%v", err)
	}
	if gone.DeletedAt == nil || !gone.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted_at 未落库：%v", gone.DeletedAt)
	}
	// 已删再删：幂等成功。
	if err := repo.MarkDeleted(ctx, m.ID, deletedAt.Add(time.Hour)); err != nil {
		t.Fatalf("重复 MarkDeleted 应幂等成功：%v", err)
	}
	// 已删后再 MarkRead：幂等成功且不写 read_at（内存实现同样直接 return nil）。
	if err := repo.MarkRead(ctx, m.ID, fixedNow()); err != nil {
		t.Fatalf("对已删行 MarkRead 应幂等成功：%v", err)
	}
}

// TestMessagePGRepo_ListCursorPaging 覆盖：createdAt 倒序 + id 倒序的游标翻页
// 不重复不遗漏，且同刻多行靠 id 兜底。
func TestMessagePGRepo_ListCursorPaging(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(9)

	// 5 条：第 2、3 条 createdAt 完全相同（考验 id 兜底）。
	same := fixedNow().Add(2 * time.Minute)
	created := []time.Time{
		fixedNow(),
		same,
		same,
		fixedNow().Add(4 * time.Minute),
		fixedNow().Add(1 * time.Minute),
	}
	var want []*message.Message
	for i, at := range created {
		m := &message.Message{
			RecipientID: recipient, Kind: message.KindInternalMail,
			Title: fmt.Sprintf("m%d", i), Body: "b", Hits: 1,
			SenderID: int64(1000 + i), CreatedAt: at,
		}
		if err := repo.Create(ctx, m); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		want = append(want, m)
	}
	// 期望顺序用领域导出的 LessForOrder 算（真源，不在测试里另写一份）。
	sortMessages(want)
	wantIDs := make([]int64, len(want))
	for i, m := range want {
		wantIDs[i] = m.ID
	}

	// 一页取全。
	all, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotIDs := make([]int64, len(all))
	for i, m := range all {
		gotIDs[i] = m.ID
	}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("排序不符：want %v got %v", wantIDs, gotIDs)
	}

	// limit=2 逐页翻完。
	var paged []int64
	seen := map[int64]bool{}
	cursor := message.PageCursor{}
	for i := 0; i < 10; i++ {
		page, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, cursor, 2)
		if err != nil {
			t.Fatalf("翻页第 %d 次: %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			if seen[m.ID] {
				t.Fatalf("游标翻页出现重复行 id=%d", m.ID)
			}
			seen[m.ID] = true
			paged = append(paged, m.ID)
		}
		last := page[len(page)-1]
		cursor = message.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if fmt.Sprint(paged) != fmt.Sprint(wantIDs) {
		t.Fatalf("翻页结果与一次取全不一致：paged %v want %v", paged, wantIDs)
	}
}

// TestMessagePGRepo_ListUnreadOnlyFilter 覆盖 MatchesFilter 的「仅未读」分支：
// UnreadOnly 为真时 SQL 里必须真的加上 read_at IS NULL。
func TestMessagePGRepo_ListUnreadOnlyFilter(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(31)

	a, err := repo.UpsertNotification(ctx, msgKey(recipient, "post", "like:1"), "已读", "b", fixedNow())
	if err != nil {
		t.Fatalf("通知1: %v", err)
	}
	if err := repo.MarkRead(ctx, a.ID, fixedNow()); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if _, err := repo.UpsertNotification(ctx, msgKey(recipient, "post", "like:2"), "未读", "b", fixedNow().Add(time.Minute)); err != nil {
		t.Fatalf("通知2: %v", err)
	}

	unread, err := repo.List(ctx, message.ListFilter{RecipientID: recipient, UnreadOnly: true}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List unread: %v", err)
	}
	if len(unread) != 1 || unread[0].Title != "未读" {
		t.Fatalf("仅未读应只返回 1 条「未读」，实际 %d 条", len(unread))
	}
	all, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("不过滤应返回 2 条，实际 %d", len(all))
	}
}

// TestMessagePGRepo_SoftDeletedNotificationStaysMerged 锁住一条**领域决策**：
// 已软删的通知仍留在部分唯一索引里，同键再次触发会合并回同一行且
// deleted_at 保持非空 → 对用户仍不可见、不计未读（「删了就不再被同一来源打扰」）。
//
// 这是内存实现的注释里明确写下的取向，PG 实现必须同样成立，
// 否则两套实现会在「删除后再触发」这个场景上分叉。
func TestMessagePGRepo_SoftDeletedNotificationStaysMerged(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	const recipient = int64(42)
	key := msgKey(recipient, "post", "like:9")

	first, err := repo.UpsertNotification(ctx, key, "赞", "b", fixedNow())
	if err != nil {
		t.Fatalf("首次: %v", err)
	}
	if err := repo.MarkDeleted(ctx, first.ID, fixedNow().Add(time.Minute)); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	second, err := repo.UpsertNotification(ctx, key, "又赞", "b2", fixedNow().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("删除后再触发: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("删除后再触发应合并回同一行：first=%d second=%d", first.ID, second.ID)
	}
	if second.Hits != 2 {
		t.Fatalf("hits 应累加为 2，实际 %d", second.Hits)
	}
	if second.DeletedAt == nil {
		t.Fatal("合并不得清空 deleted_at（删了就不再被同一来源打扰）")
	}
	// 不可见、不计未读。
	if n, _ := repo.UnreadCount(ctx, message.ListFilter{RecipientID: recipient}); n != 0 {
		t.Fatalf("软删的通知不应计未读，实际 %d", n)
	}
	list, err := repo.List(ctx, message.ListFilter{RecipientID: recipient}, message.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("软删的通知不应出现在列表里，实际 %d 条", len(list))
	}
}

// ── 本文件用到的辅助 ──────────────────────────────────────────────

// sortMessages 用领域导出的 LessForOrder 排序（测试侧真源判定）。
func sortMessages(list []*message.Message) {
	sortSlice(list, func(a, b *message.Message) bool { return message.LessForOrder(a, b) })
}

// TestMessagePGRepo_ErrorIsDomainError 锁住「repo 的 NotFound 是领域错误」：
// handler 靠 apperr.CodeOf 把它映射成 404，若返回裸 pgx.ErrNoRows 就会变成 500。
func TestMessagePGRepo_ErrorIsDomainError(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if repo == nil {
		return
	}
	_, err := repo.FindByID(context.Background(), 987654)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !errors.Is(err, error(apperr.Errorf(apperr.CodeNotFound, "x"))) && err.Error() == "" {
		t.Fatalf("错误不应为空：%v", err)
	}
	if _, ok := apperr.CodeOf(err); !ok {
		t.Fatalf("应是带错误码的领域错误，实际 %T: %v", err, err)
	}
}
