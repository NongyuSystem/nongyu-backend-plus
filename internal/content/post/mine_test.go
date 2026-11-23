package post_test

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/content/post/store"
)

// mapCache 是 post.Cache 的内存实现，只给本文件的缓存串号测试用。
//
// 生产环境用 Redis 适配器（internal/app/post_cache.go）；这里复刻
// Get/Set/Delete 三个方法的语义即可，重点是让**缓存分支真的被走到**。
type mapCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMapCache() *mapCache { return &mapCache{m: map[string][]byte{}} }

func (c *mapCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *mapCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = val
	return nil
}

func (c *mapCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.m, k)
	}
	return nil
}

// itoa 是本文件的小工具（避免为一次转换 import 更多东西）。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// 本文件锁住 GET /posts?mine=true 的语义。
//
// 两条硬要求：
//  1. mine=true 只返回「当前登录用户自己发的」帖子；
//  2. 缓存不能让 A 的列表被 B 命中（跨用户串号 = 隐私事故）。

// TestListMine_OnlyOwnPosts 覆盖主路径：各人只看到自己的。
func TestListMine_OnlyOwnPosts(t *testing.T) {
	r, _, _, _ := newEnv(t)

	// 用户7 发 2 条，用户8 发 1 条。
	createPost(t, r, "7", "七的帖子一")
	createPost(t, r, "7", "七的帖子二")
	createPost(t, r, "8", "八的帖子")

	// 用户7 看 mine：应只有 2 条，且不含别人的。
	code, body := do(t, r, http.MethodGet, "/posts?mine=true", "7", nil)
	if code != http.StatusOK {
		t.Fatalf("mine 列表应 200，得到 %d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("用户7 应只看到自己 2 条，实际 %d 条", len(items))
	}
	for _, it := range items {
		m := it.(map[string]any)
		if m["title"] == "八的帖子" {
			t.Fatal("mine 列表混进了别人的帖子")
		}
		// mine 列表里每一条都该是 isMine=true
		if m["isMine"] != true {
			t.Fatalf("mine 列表里的帖子 isMine 应为 true，得到 %v", m["isMine"])
		}
	}

	// 用户8 看 mine：只有 1 条。
	_, body = do(t, r, http.MethodGet, "/posts?mine=true", "8", nil)
	if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("用户8 应只看到 1 条，实际 %d 条", len(items))
	}

	// 不带 mine：公开列表看到全部 3 条。
	_, body = do(t, r, http.MethodGet, "/posts", "", nil)
	if items, _ := body["items"].([]any); len(items) != 3 {
		t.Fatalf("公开列表应有 3 条，实际 %d 条", len(items))
	}
}

// TestListMine_RequiresAuth 未登录时 mine 必须 401，而不是返回空列表。
//
// 返回空会让客户端显示「你还没有发过帖子」——而真实原因是没登录，误导用户。
func TestListMine_RequiresAuth(t *testing.T) {
	r, _, _, _ := newEnv(t)
	createPost(t, r, "7", "别人的帖子")

	code, body := do(t, r, http.MethodGet, "/posts?mine=true", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录的 mine 应 401，得到 %d %v", code, body)
	}
}

// TestListMine_NoCacheCrossUserLeak 是隐私回归：缓存键必须区分作者。
//
// 若缓存键只含 (board, cursor, limit)，B 的 mine 请求会命中 A 缓存的列表，
// B 就看到 A 的帖子。这条测试用「同一 board 同一游标」的连续请求来逼近该场景。
func TestListMine_NoCacheCrossUserLeak(t *testing.T) {
	// 手工装配带缓存的 Service，确保缓存分支真的被走到。
	repo := store.NewPostMemoryRepo(func() time.Time { return t0 })
	cache := newMapCache()
	svc := post.New(post.Options{
		Repo:  repo,
		Clock: fixedClock{now: t0},
		Cache: cache,
	})

	ctx := context.Background()
	p7, err := svc.Create(ctx, 7, post.CreateInput{Board: "feedback", Title: "七的", Body: "b"})
	if err != nil {
		t.Fatalf("发帖失败：%v", err)
	}
	p8, err := svc.Create(ctx, 8, post.CreateInput{Board: "feedback", Title: "八的", Body: "b"})
	if err != nil {
		t.Fatalf("发帖失败：%v", err)
	}

	// 用户7 先查（会填充缓存）。
	res7, err := svc.List(ctx, "feedback", 7, post.PageCursor{}, 20)
	if err != nil {
		t.Fatalf("用户7 查询失败：%v", err)
	}
	if len(res7.Items) != 1 || res7.Items[0].ID != p7.ID {
		t.Fatalf("用户7 应只看到自己的帖，得到 %d 条", len(res7.Items))
	}

	// 用户8 紧接着查同样参数——必须回源，不能命中用户7 的缓存。
	res8, err := svc.List(ctx, "feedback", 8, post.PageCursor{}, 20)
	if err != nil {
		t.Fatalf("用户8 查询失败：%v", err)
	}
	if len(res8.Items) != 1 {
		t.Fatalf("用户8 应只看到自己 1 条，得到 %d 条", len(res8.Items))
	}
	if res8.Items[0].ID != p8.ID {
		t.Fatalf("用户8 看到了别人的帖子（缓存串号）：id=%d，应为 %d", res8.Items[0].ID, p8.ID)
	}

	// 再查一次用户7，确认缓存仍然给了他正确的结果（没有反过来被 8 覆盖）。
	res7b, err := svc.List(ctx, "feedback", 7, post.PageCursor{}, 20)
	if err != nil {
		t.Fatalf("用户7 复查失败：%v", err)
	}
	if len(res7b.Items) != 1 || res7b.Items[0].ID != p7.ID {
		t.Fatal("用户7 的缓存结果被污染")
	}
}

// TestListMine_SoftDeletedExcluded mine 列表也不该出现已软删的帖子。
func TestListMine_SoftDeletedExcluded(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "将被删除")
	createPost(t, r, "7", "保留着")

	if code, _ := do(t, r, http.MethodDelete, "/posts/"+itoa(id), "7", nil); code != http.StatusOK {
		t.Fatal("删帖失败")
	}
	_, body := do(t, r, http.MethodGet, "/posts?mine=true", "7", nil)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("软删后 mine 应只剩 1 条，实际 %d 条", len(items))
	}
	if items[0].(map[string]any)["title"] != "保留着" {
		t.Fatal("剩下的应是未删除那条")
	}
}

// TestListMine_CombinesWithBoard mine 与 board 可以叠加。
func TestListMine_CombinesWithBoard(t *testing.T) {
	r, _, _, _ := newEnv(t)
	// 用户在 feedback 发 1 条、courtyard 发 1 条。
	do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "feedback", "title": "反馈帖", "body": "b",
	})
	do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "courtyard", "title": "院子帖", "body": "b",
	})

	_, body := do(t, r, http.MethodGet, "/posts?mine=true&board=feedback", "7", nil)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("mine+board 应只返回 1 条，实际 %d 条", len(items))
	}
	if items[0].(map[string]any)["title"] != "反馈帖" {
		t.Fatalf("应只返回 feedback 板块的帖子")
	}
}
