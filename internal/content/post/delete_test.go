package post_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// 本文件锁住删除权限边界（2026-11-23 确认）：
//   - 帖子：普通用户只能删自己的；管理端可删任意；
//   - 评论：作者能删自己的；**帖主不能删别人在自己帖下的评论**；
//   - 删帖级联软删该帖下全部评论。
//
// 非作者一律 404 而不是 403：不泄露「这条内容存在但不是你的」。

// seedPostWithReply 造一条帖子 + 一条评论，返回两个 id。
func seedPostWithReply(t *testing.T, r *gin.Engine, authorUID string) (postID, replyID int64) {
	t.Helper()
	code, body := do(t, r, http.MethodPost, "/posts", authorUID, map[string]any{
		"board": "courtyard", "title": "t", "body": "b",
	})
	if code != http.StatusCreated {
		t.Fatalf("造帖失败：%d", code)
	}
	postID = int64(body["data"].(map[string]any)["id"].(float64))

	code, body = do(t, r, http.MethodPost, "/posts/"+itoa(postID)+"/replies", authorUID,
		map[string]any{"body": "评论内容"})
	if code != http.StatusCreated {
		t.Fatalf("造评论失败：%d %v", code, body)
	}
	replyID = int64(body["data"].(map[string]any)["id"].(float64))
	return
}

func TestDeleteReply_AuthorCanDeleteOwn(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	code, _ := do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid)+"/replies/"+itoa(rid), "7", nil)
	if code != http.StatusOK {
		t.Fatalf("作者删自己的评论应 200，得到 %d", code)
	}

	// 再查详情：评论应不再出现。
	_, body := do(t, r, http.MethodGet, "/posts/"+itoa(pid), "7", nil)
	replies, _ := body["replies"].([]any)
	if len(replies) != 0 {
		t.Fatalf("已删评论不该出现，得到 %d 条", len(replies))
	}
}

func TestDeleteReply_NonAuthorGets404(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	// 用户 9 不是评论作者。
	code, body := do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid)+"/replies/"+itoa(rid), "9", nil)
	if code != http.StatusNotFound {
		t.Fatalf("非作者删评论应 404（而非 403），得到 %d %v", code, body)
	}
}

func TestDeleteReply_WrongParentPostGets404(t *testing.T) {
	r, _, _, _ := newEnv(t)
	_, rid := seedPostWithReply(t, r, "7")
	pid2, _ := seedPostWithReply(t, r, "7")

	// 拿第二帖的 id 去删第一帖的评论 → 必须拒绝。
	code, body := do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid2)+"/replies/"+itoa(rid), "7", nil)
	if code != http.StatusNotFound {
		t.Fatalf("父帖不匹配应 404，得到 %d %v", code, body)
	}
}

func TestDeleteReply_PostOwnerCannotDeleteOthersReply(t *testing.T) {
	r, _, _, _ := newEnv(t)
	// 帖子作者是 7，评论作者是 8。
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "courtyard", "title": "t", "body": "b",
	})
	if code != http.StatusCreated {
		t.Fatalf("造帖失败：%d", code)
	}
	pid := int64(body["data"].(map[string]any)["id"].(float64))
	code, body = do(t, r, http.MethodPost, "/posts/"+itoa(pid)+"/replies", "8",
		map[string]any{"body": "别人的评论"})
	if code != http.StatusCreated {
		t.Fatalf("造评论失败：%d %v", code, body)
	}
	rid := int64(body["data"].(map[string]any)["id"].(float64))

	// 帖主 7 试图删评论作者 8 的评论 → 拒绝。
	code, _ = do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid)+"/replies/"+itoa(rid), "7", nil)
	if code != http.StatusNotFound {
		t.Fatalf("帖主不能删别人的评论，应 404，得到 %d", code)
	}
}

func TestDeleteReply_Idempotent(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	path := "/posts/" + itoa(pid) + "/replies/" + itoa(rid)
	if code, _ := do(t, r, http.MethodDelete, path, "7", nil); code != http.StatusOK {
		t.Fatal("首次删除应 200")
	}
	// 重复删除应幂等成功。
	if code, _ := do(t, r, http.MethodDelete, path, "7", nil); code != http.StatusOK {
		t.Fatalf("重复删除应幂等 200，得到 %d", code)
	}
}

func TestDeleteReply_RequiresAuth(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	code, _ := do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid)+"/replies/"+itoa(rid), "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录删评论应 401，得到 %d", code)
	}
}

func TestDeleteReply_DeletedPostRejects(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	// 先删帖（会级联软删评论）。
	if code, _ := do(t, r, http.MethodDelete, "/posts/"+itoa(pid), "7", nil); code != http.StatusOK {
		t.Fatal("删帖失败")
	}
	// 帖子已删后，再单独删评论应被拒（帖子不存在）。
	code, _ := do(t, r, http.MethodDelete,
		"/posts/"+itoa(pid)+"/replies/"+itoa(rid), "7", nil)
	if code != http.StatusNotFound {
		t.Fatalf("已删帖子下的评论删除应 404，得到 %d", code)
	}
}

// TestDeletePost_CascadesReplies 普通用户删帖时级联软删评论。
func TestDeletePost_CascadesReplies(t *testing.T) {
	r, svc, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	if code, _ := do(t, r, http.MethodDelete, "/posts/"+itoa(pid), "7", nil); code != http.StatusOK {
		t.Fatal("删帖失败")
	}

	// 直接查 repo：评论的 DeletedAt 应已设置。
	rp, err := svc.FindReply(context.Background(), rid)
	if err != nil {
		t.Fatalf("查评论失败：%v", err)
	}
	if rp.DeletedAt == nil {
		t.Fatal("删帖应级联软删其下评论")
	}
}

// TestAdminDeletePost_CascadesReplies 管理端删帖同样级联。
func TestAdminDeletePost_CascadesReplies(t *testing.T) {
	r, svc, _, _ := newEnv(t)
	pid, rid := seedPostWithReply(t, r, "7")

	// 用另一个用户走管理端删除（管理端不校验作者）。
	code, _ := do(t, r, http.MethodDelete, "/admin/posts/"+itoa(pid), "999", nil)
	if code != http.StatusOK {
		t.Fatalf("管理端删帖应 200，得到 %d", code)
	}

	rp, err := svc.FindReply(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	if rp.DeletedAt == nil {
		t.Fatal("管理端删帖也应级联软删评论")
	}
}

// TestAdminDeletePost_CanDeleteOthers 管理端可删任意人的帖子。
func TestAdminDeletePost_CanDeleteOthers(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, _ := seedPostWithReply(t, r, "7")

	code, _ := do(t, r, http.MethodDelete, "/admin/posts/"+itoa(pid), "999", nil)
	if code != http.StatusOK {
		t.Fatalf("管理端应能删他人帖子，得到 %d", code)
	}
}

// TestDeletePost_NonAuthorGets404 普通用户删他人帖子得到 404。
func TestDeletePost_NonAuthorGets404(t *testing.T) {
	r, _, _, _ := newEnv(t)
	pid, _ := seedPostWithReply(t, r, "7")

	code, _ := do(t, r, http.MethodDelete, "/posts/"+itoa(pid), "8", nil)
	if code != http.StatusNotFound {
		t.Fatalf("非作者删帖应 404，得到 %d", code)
	}
}
