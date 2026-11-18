package jiaowu_test

// 定时续期循环的针对性测试：Plan/PlanningWindow 必须有生产调用点，
// 且循环可优雅关停、不静默重试刷屏。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

func TestSchedulerRunOncePicksCandidatesFromObservedWindow(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		// 单测注入 no-op 等待：错峰延迟是秒级，测试不能真等。
		WithPacing(func(context.Context, time.Duration) bool { return true }, func() time.Duration { return 0 })

	// 3 个即将过期用户，1 个远期用户（不该被挑中）。
	for _, u := range []string{"u-a", "u-b", "u-c"} {
		if err := tickets.Put(nil, u, []byte("sealed-"+u), clock.Now().Add(1*time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := tickets.Put(nil, "u-far", []byte("sealed"), clock.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var mu sync.Mutex
	var executed []string
	count, err := sched.RunOnce(context.Background(), func(ctx context.Context, u string) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, u)
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 3 {
		t.Fatalf("应挑出观测窗口内 3 个候选，实际 %d（executed=%v）", count, executed)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, u := range executed {
		if u == "u-far" {
			t.Fatalf("远期用户不应被挑中：%v", executed)
		}
	}
}

func TestSchedulerRunStopsCleanly(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock)

	ctx, cancel := context.WithCancel(context.Background())
	var executed int64
	var mu sync.Mutex
	done := sched.Run(ctx, 10*time.Millisecond, func(_ context.Context, _ string) {
		mu.Lock()
		executed++
		mu.Unlock()
	})

	// 放一个候选，让循环有事可做。
	if err := tickets.Put(nil, "u-run", []byte("sealed"), clock.Now().Add(1*time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(35 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// 优雅退出
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run 未在 ctx 取消后及时退出")
	}
	// Stop 幂等，二次调用不 panic。
	sched.Stop()
}

// ── 错峰执行守卫（2026-11-10）─────────────────────────────────────
//
// 背景：Plan 早就按 userID 哈希算出 16 个错峰槽位，但 RunOnce 一直直接同步
// 逐个 exec，**Offset 算了却没用上**——一轮 64 个候选仍会在同一瞬间全打出去，
// 对学校侧就是一次 64 并发尖峰。这两条测试锁住「槽位真的被用起来了」。

func TestSchedulerRunOnce_StaggersAcrossSlots(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))

	// 塞足够多的候选，确保散列后落在多个槽位。
	for i := 0; i < 30; i++ {
		u := fmt.Sprintf("u-%02d", i)
		if err := tickets.Put(nil, u, []byte("sealed"), clock.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	var mu sync.Mutex
	var waits []time.Duration
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		WithPacing(
			func(_ context.Context, d time.Duration) bool {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
				return true
			},
			func() time.Duration { return 0 },
		)

	executed := 0
	count, err := sched.RunOnce(context.Background(), func(context.Context, string) { executed++ })
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 30 || executed != 30 {
		t.Fatalf("应处理 30 个候选，count=%d executed=%d", count, executed)
	}

	mu.Lock()
	nWaits := len(waits)
	mu.Unlock()
	// 30 个候选按 16 槽散列，实际槽位数应在 2..16 之间（哈希不保证铺满）。
	// 关键断言：**确实发生过等待**（槽位之间被拉开），而不是一次性全跑完。
	if nWaits == 0 {
		t.Fatal("跨槽位时应至少等待一次（否则等于没有错峰）")
	}
	if nWaits >= count {
		t.Fatalf("等待次数(%d)不应达到候选数(%d)——槽位内应连续执行，只在槽位间等", nWaits, count)
	}
}

func TestSchedulerRunOnce_StopsOnContextCancel(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 30; i++ {
		u := fmt.Sprintf("u-%02d", i)
		if err := tickets.Put(nil, u, []byte("sealed"), clock.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 第一次等待就取消：模拟关停时正在错峰。
	sched := jiaowu.NewRenewScheduler(tickets, clock).
		WithPacing(
			func(context.Context, time.Duration) bool {
				cancel()
				return false
			},
			func() time.Duration { return 0 },
		)

	executed := 0
	count, err := sched.RunOnce(ctx, func(context.Context, string) { executed++ })
	if err != nil {
		t.Fatalf("RunOnce 不该因取消而报错：%v", err)
	}
	// 已处理的必须小于总数：取消后不再继续对外发请求。
	if executed >= 30 || count >= 30 {
		t.Fatalf("ctx 取消后应停止剩余候选，实际 executed=%d count=%d", executed, count)
	}
}
