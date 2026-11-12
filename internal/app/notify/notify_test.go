package notify

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这一层锁住 #48 Q3/Q5 的旁路语义：
// 队列满/写入失败/发给自己都只能「丢并计数」，绝不阻塞主业务、绝不回抛。

func TestEnqueue_FiltersSelfSend(t *testing.T) {
	var got atomic.Int64
	n := New(Options{
		Sink:     SinkFunc(func(_ context.Context, _ Event) error { got.Add(1); return nil }),
		Capacity: 8,
	})
	defer n.Flush()

	// ActorID == RecipientID：编排层第一层过滤，不该进 Sink。
	n.Enqueue(Event{RecipientID: 7, ActorID: 7, SourceDomain: "post", SourceObjectID: "like:post:1"})
	// 正常一条（actor 不同）。
	n.Enqueue(Event{RecipientID: 7, ActorID: 9, SourceDomain: "post", SourceObjectID: "like:post:1"})

	n.Flush()
	if n.Emitted() != 1 {
		t.Fatalf("应只投递 1 条（自发自收被过滤），得到 emitted=%d", n.Emitted())
	}
}

func TestEnqueue_QueueFullDropsWithoutBlocking(t *testing.T) {
	// 用一个「遵守 ctx」的 Sink 把后台协程卡住，逼队列堆满。
	// Sink 尊重 ctx.Done() 是编排层 deliver 用 WithTimeout 传参的前提，
	// 真实实现也必须如此；否则下游慢会卡死后台协程。
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	n := New(Options{
		Sink: SinkFunc(func(ctx context.Context, _ Event) error {
			// 非阻塞通知「已启动」：drain 阶段还会再调 Sink 一次，
			// 若这里阻塞式发送，第二次就会卡死（测试自身的死锁）。
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		Capacity: 2,
	})

	// 先塞一条让后台协程进入 Sink 并卡住。
	n.Enqueue(Event{RecipientID: 1, ActorID: 2, SourceDomain: "post", SourceObjectID: "x:1"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("后台协程未启动")
	}

	// 队列容量 2，连续塞 20 条：多于容量的必须走 default 丢弃。
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			n.Enqueue(Event{RecipientID: int64(i + 10), ActorID: 2,
				SourceDomain: "post", SourceObjectID: "x:full"})
		}
		close(done)
	}()

	select {
	case <-done:
		// 关键：Enqueue 没有因为队列满而阻塞。
	case <-time.After(2 * time.Second):
		t.Fatal("队列满时 Enqueue 阻塞了主业务（违反旁路语义）")
	}

	if n.Dropped() == 0 {
		t.Fatal("队列满应产生 dropped 计数")
	}

	close(release)
	n.Flush()
}

func TestDeliver_SinkErrorIsDropped(t *testing.T) {
	n := New(Options{
		Sink:     SinkFunc(func(_ context.Context, _ Event) error { return errors.New("下游故障") }),
		Capacity: 8,
	})
	n.Enqueue(Event{RecipientID: 7, ActorID: 9, SourceDomain: "post", SourceObjectID: "like:post:1"})
	n.Flush()

	if n.Dropped() != 1 {
		t.Fatalf("Sink 失败应计入 dropped，得到 %d", n.Dropped())
	}
	if n.Emitted() != 0 {
		t.Fatalf("Sink 失败不该计入 emitted，得到 %d", n.Emitted())
	}
}

func TestDeliver_NilSinkDrops(t *testing.T) {
	// Sink 为 nil 时不能 panic，且必须计入 dropped。
	n := New(Options{Capacity: 4})
	n.Enqueue(Event{RecipientID: 7, ActorID: 9, SourceDomain: "post", SourceObjectID: "x"})
	n.Flush()
	if n.Dropped() != 1 {
		t.Fatalf("nil Sink 应丢弃并计数，得到 %d", n.Dropped())
	}
}

func TestFlush_IsIdempotent(t *testing.T) {
	n := New(Options{Sink: SinkFunc(func(_ context.Context, _ Event) error { return nil }), Capacity: 8})
	n.Enqueue(Event{RecipientID: 1, ActorID: 2, SourceDomain: "post", SourceObjectID: "x"})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); n.Flush() }()
	}
	wg.Wait() // 并发多次 Flush 不能 panic、不能死锁
}

func TestEnqueue_ZeroRecipientIgnored(t *testing.T) {
	n := New(Options{Sink: SinkFunc(func(_ context.Context, _ Event) error { return nil }), Capacity: 8})
	defer n.Flush()
	n.Enqueue(Event{RecipientID: 0, ActorID: 2, SourceDomain: "post", SourceObjectID: "x"})
	n.Flush()
	if n.Emitted() != 0 || n.Dropped() != 0 {
		t.Fatalf("收件人为 0 应直接忽略（不算丢弃），emitted=%d dropped=%d", n.Emitted(), n.Dropped())
	}
}
