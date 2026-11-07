package jiaowu_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// 定时续期循环的针对性测试：Plan/PlanningWindow 必须有生产调用点，
// 且循环可优雅关停、不静默重试刷屏。

func TestSchedulerRunOncePicksCandidatesFromObservedWindow(t *testing.T) {
	tickets := platform.NewMockTicketStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	sched := jiaowu.NewRenewScheduler(tickets, clock)

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
