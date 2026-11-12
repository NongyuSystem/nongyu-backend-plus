package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// 本文件锁住「公告读者画像」这条链路。
//
// 背景：announcementViewerOf 曾**只填角色、学院/年级留空**，
// 于是「按学院/年级定向」的公告对所有人不可见。那是 fail-closed 的刻意选择
// （把空当成通配会让定向公告退化成全员公告，是隐私事故），
// 但也意味着功能一直不可用。PG 仓储与身份域就位后补上了这条链路。

// fakeUserRepo 只实现 ByID（其余方法 panic，保证不被意外调用）。
type fakeUserRepo struct {
	platform.UserRepo
	users map[int64]platform.User
	err   error
}

func (f fakeUserRepo) ByID(_ context.Context, id int64) (platform.User, error) {
	if f.err != nil {
		return platform.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return platform.User{}, errors.New("not found")
	}
	return u, nil
}

// fakeBriefReader 返回固定画像。
type fakeBriefReader struct {
	briefs map[string]platform.StudentBrief
	err    error
	calls  *int
}

func (f fakeBriefReader) Brief(_ context.Context, studentNo string) (platform.StudentBrief, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.err != nil {
		return platform.StudentBrief{}, f.err
	}
	b, ok := f.briefs[studentNo]
	if !ok {
		return platform.StudentBrief{}, errors.New("no profile")
	}
	return b, nil
}

// TestProfileResolver_ResolvesCollegeAndGrade 覆盖主路径：两跳都成功。
func TestProfileResolver_ResolvesCollegeAndGrade(t *testing.T) {
	users := fakeUserRepo{users: map[int64]platform.User{
		42: {ID: 42, StudentNo: "2022001"},
	}}
	briefs := fakeBriefReader{briefs: map[string]platform.StudentBrief{
		"2022001": {StudentNo: "2022001", College: "计算机学院", Grade: "2022"},
	}}

	resolve := newProfileResolver(users, briefs)
	b, ok := resolve(context.Background(), 42)
	if !ok {
		t.Fatal("应解析出画像")
	}
	if b.College != "计算机学院" || b.Grade != "2022" {
		t.Fatalf("画像不符：%+v", b)
	}
}

// TestProfileResolver_NoStudentNoFailsClosed 教职工账号（无学号）应无画像。
//
// 关键：这时**不能**返回空画像当成「命中所有学院」——那会让定向公告
// 对教职工可见。返回 ok=false，调用方保持 College/Grade 为空 = 不命中。
func TestProfileResolver_NoStudentNoFailsClosed(t *testing.T) {
	users := fakeUserRepo{users: map[int64]platform.User{
		7: {ID: 7, StudentNo: ""}, // 教职工
	}}
	briefs := fakeBriefReader{}
	resolve := newProfileResolver(users, briefs)

	if _, ok := resolve(context.Background(), 7); ok {
		t.Fatal("无学号账号不该解析出画像（否则定向公告会误可见）")
	}
}

// TestProfileResolver_TemporaryFailureIsNotCached 临时故障不缓存。
//
// 若把「查不到画像」也缓存 5 分钟，一次 DB 抖动会让该用户
// 5 分钟内一直看不到定向公告。
func TestProfileResolver_TemporaryFailureIsNotCached(t *testing.T) {
	users := fakeUserRepo{users: map[int64]platform.User{
		42: {ID: 42, StudentNo: "2022001"},
	}}
	calls := 0
	briefs := fakeBriefReader{
		briefs: map[string]platform.StudentBrief{},
		err:    errors.New("db down"), // 第一次总是失败
		calls:  &calls,
	}
	resolve := newProfileResolver(users, briefs)

	// 连续两次都应真的去打画像接口（没被缓存成 found=false）
	_, _ = resolve(context.Background(), 42)
	_, _ = resolve(context.Background(), 42)
	if calls != 2 {
		t.Fatalf("临时故障不该被缓存，画像接口应被调 2 次，实际 %d 次", calls)
	}
}

// TestProfileResolver_CachesSuccessfulLookup 成功结果应缓存（读放大防护）。
func TestProfileResolver_CachesSuccessfulLookup(t *testing.T) {
	users := fakeUserRepo{users: map[int64]platform.User{
		42: {ID: 42, StudentNo: "2022001"},
	}}
	calls := 0
	briefs := fakeBriefReader{
		briefs: map[string]platform.StudentBrief{
			"2022001": {College: "计算机学院", Grade: "2022"},
		},
		calls: &calls,
	}
	resolve := newProfileResolver(users, briefs)

	for i := 0; i < 5; i++ {
		if _, ok := resolve(context.Background(), 42); !ok {
			t.Fatal("应解析出画像")
		}
	}
	// 5 次请求只应打 1 次画像接口。
	if calls != 1 {
		t.Fatalf("成功结果应缓存，5 次解析应只调 1 次，实际 %d 次", calls)
	}
}

// TestNewProfileResolver_NilDepsReturnsNil 缺依赖时应返回 nil 而不是 panic。
func TestNewProfileResolver_NilDepsReturnsNil(t *testing.T) {
	if newProfileResolver(nil, fakeBriefReader{}) != nil {
		t.Fatal("缺 UserRepo 应返回 nil")
	}
	if newProfileResolver(fakeUserRepo{}, nil) != nil {
		t.Fatal("缺 BriefReader 应返回 nil")
	}
}

// TestProfileCache_Expires 保证 TTL 生效（改了学院之后最终能看到新值）。
func TestProfileCache_Expires(t *testing.T) {
	c := &profileCache{m: map[int64]profileEntry{}}
	c.put(1, profileEntry{
		brief: platform.StudentBrief{College: "旧学院"},
		found: true,
	}, time.Minute)

	if e, ok := c.get(1); !ok || e.brief.College != "旧学院" {
		t.Fatal("刚写入应命中")
	}

	// 手工把时间戳推老，模拟过期。
	c.mu.Lock()
	e := c.m[1]
	e.at = time.Now().Add(-10 * time.Minute)
	c.m[1] = e
	c.mu.Unlock()

	if _, ok := c.get(1); ok {
		t.Fatal("超过 TTL 应视为未命中")
	}
}
