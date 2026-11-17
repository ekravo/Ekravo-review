package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPool() *Pool {
	return New(1, 10, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSingleFlightPerKey(t *testing.T) {
	p := testPool()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	var runs atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	job := func(context.Context) {
		runs.Add(1)
		close(started)
		<-release
	}

	if !p.Submit("repo#1", job) {
		t.Fatal("first submit rejected")
	}
	<-started

	if p.Submit("repo#1", job) {
		t.Fatal("duplicate key should be rejected while running")
	}
	if !p.Submit("repo#2", func(context.Context) {}) {
		t.Fatal("different key should be accepted")
	}

	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for p.Inflight() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("inflight not cleared: %d", p.Inflight())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1", runs.Load())
	}
	p.Shutdown(context.Background())
}

func TestQueueFullIsRejectedWithoutBlocking(t *testing.T) {
	p := New(1, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Not started: one job fills the buffer, the next is rejected.
	block := make(chan struct{})
	defer close(block)

	if !p.Submit("a", func(context.Context) {}) {
		t.Fatal("first submit rejected")
	}
	if p.Submit("b", func(context.Context) {}) {
		t.Fatal("full queue should reject")
	}
}

func TestShutdownRunsQueuedJobs(t *testing.T) {
	p := testPool()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	var ran atomic.Int32
	for _, key := range []string{"a", "b", "c"} {
		if !p.Submit(key, func(context.Context) { ran.Add(1) }) {
			t.Fatalf("submit %s rejected", key)
		}
	}
	p.Shutdown(context.Background())

	if ran.Load() != 3 {
		t.Fatalf("ran = %d, want 3", ran.Load())
	}
}

func waitInflightZero(t *testing.T, p *Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for p.Inflight() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("inflight not cleared: %d", p.Inflight())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestJoinDuringRunRerunsLatestJob(t *testing.T) {
	p := testPool()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	var mu sync.Mutex
	var order []string
	started := make(chan struct{})
	release := make(chan struct{})
	first := func(context.Context) {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		close(started)
		<-release
	}

	if !p.Submit("repo#1", first) {
		t.Fatal("first submit rejected")
	}
	<-started

	for _, sha := range []string{"old", "new"} {
		sha := sha
		if err := p.SubmitOrJoin("repo#1", func(context.Context) {
			mu.Lock()
			order = append(order, sha)
			mu.Unlock()
		}); err != nil {
			t.Fatalf("join while running rejected: %v", err)
		}
	}
	close(release)

	waitInflightZero(t, p)
	p.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "new" {
		t.Fatalf("runs = %v, want [first new]: joins must collapse to one rerun with the latest job", order)
	}
}

func TestJoinKeepsKeyInflightUntilPendingDone(t *testing.T) {
	p := testPool()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	started := make(chan struct{})
	release := make(chan struct{})
	if !p.Submit("repo#1", func(context.Context) {
		close(started)
		<-release
	}) {
		t.Fatal("first submit rejected")
	}
	<-started

	pendingRan := make(chan struct{})
	if err := p.SubmitOrJoin("repo#1", func(context.Context) { close(pendingRan) }); err != nil {
		t.Fatalf("join rejected: %v", err)
	}
	if p.Submit("repo#1", func(context.Context) {}) {
		t.Fatal("Submit must still report busy while a pending job exists")
	}
	close(release)

	select {
	case <-pendingRan:
	case <-time.After(2 * time.Second):
		t.Fatal("pending job never ran")
	}
	waitInflightZero(t, p)
	p.Shutdown(context.Background())
}

func TestJoinAfterRerunStartedRunsAgain(t *testing.T) {
	p := testPool()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	if !p.Submit("repo#1", func(context.Context) {
		close(firstStarted)
		<-releaseFirst
	}) {
		t.Fatal("first submit rejected")
	}
	<-firstStarted

	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var thirdRan atomic.Int32
	if err := p.SubmitOrJoin("repo#1", func(context.Context) {
		close(secondStarted)
		<-releaseSecond
	}); err != nil {
		t.Fatalf("join rejected: %v", err)
	}
	close(releaseFirst)
	<-secondStarted

	// A push during the rerun must schedule yet another run.
	if err := p.SubmitOrJoin("repo#1", func(context.Context) { thirdRan.Add(1) }); err != nil {
		t.Fatalf("join during rerun rejected: %v", err)
	}
	close(releaseSecond)

	waitInflightZero(t, p)
	p.Shutdown(context.Background())
	if thirdRan.Load() != 1 {
		t.Fatalf("third run = %d, want 1", thirdRan.Load())
	}
}

func TestConcurrentSubmitShutdownNeverPanics(t *testing.T) {
	for round := 0; round < 20; round++ {
		p := New(2, 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		p.Start(ctx)

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					key := "repo#" + strconv.Itoa((g+i)%3)
					job := func(context.Context) {}
					if i%2 == 0 {
						p.Submit(key, job) // false is fine: busy or closed
						continue
					}
					err := p.SubmitOrJoin(key, job)
					if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrFull) {
						t.Errorf("unexpected submit error: %v", err)
					}
				}
			}(g)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond)
			p.Shutdown(context.Background())
		}()
		wg.Wait()
		cancel()
	}
}
