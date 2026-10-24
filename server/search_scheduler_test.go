package server

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func testScheduler(workers, queue int) *searchScheduler {
	b := baseSearchBudget(2, 384*MiB)
	b.Workers = workers
	b.Queue = queue
	b.QueueWait = time.Second
	return newSearchScheduler(b)
}

type admission struct {
	id      string
	release func()
	err     error
}

func enqueue(s *searchScheduler, ctx context.Context, id string, out chan admission) {
	go func() { r, _, e := s.acquire(ctx, id, "test"); out <- admission{id, r, e} }()
}
func TestSchedulerBorrowingFairnessAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(4, 32)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		holds := []func(){}
		for range 4 {
			r, _, e := s.acquire(ctx, "hot", "test")
			if e != nil {
				t.Fatal(e)
			}
			holds = append(holds, r)
		}
		out := make(chan admission, 12)
		for range 8 {
			enqueue(s, ctx, "hot", out)
		}
		enqueue(s, ctx, "quiet", out)
		enqueue(s, ctx, "third", out)
		synctest.Wait()
		if s.active != 4 || s.waiting != 10 {
			t.Fatalf("pool not fully borrowed: %v", s.stats())
		}
		holds[0]()
		synctest.Wait()
		a := <-out
		if a.id == "hot" {
			t.Fatal("quiet community starved behind hot backlog")
		}
		holds[1]()
		synctest.Wait()
		b := <-out
		if b.id == "hot" || b.id == a.id {
			t.Fatal("equal contenders did not receive a turn")
		}
		cancel()
		a.release()
		b.release()
		holds[2]()
		holds[3]()
		synctest.Wait()
		for len(out) > 0 {
			v := <-out
			if v.release != nil {
				v.release()
			}
		}
		if s.active != 0 || s.waiting != 0 || len(s.groups) != 0 {
			t.Fatalf("leaked state: %v", s.stats())
		}
	})
}
func TestSchedulerFloodPushoutAndBoundedQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 4)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hold, _, _ := s.acquire(ctx, "hot", "test")
		out := make(chan admission, 8)
		for range 4 {
			enqueue(s, ctx, "hot", out)
		}
		synctest.Wait()
		enqueue(s, ctx, "cold", out)
		synctest.Wait()
		victim := <-out
		if victim.err == nil || victim.id != "hot" || s.waiting != 4 {
			t.Fatalf("queue reservation failed: %v", s.stats())
		}
		hold()
		synctest.Wait()
		a := <-out
		if a.id != "cold" || a.err != nil {
			t.Fatal("new community did not get service")
		}
		cancel()
		a.release()
		synctest.Wait()
		for len(out) > 0 {
			v := <-out
			if v.release != nil {
				v.release()
			}
		}
		if s.active != 0 || s.waiting != 0 || len(s.groups) != 0 {
			t.Fatal("queue state leaked")
		}
	})
}
func TestSchedulerDeadlinesCancellationAndDoubleRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 2)
		hold, _, _ := s.acquire(context.Background(), "a", "test")
		out := make(chan admission, 2)
		ctx, cancel := context.WithCancel(context.Background())
		enqueue(s, ctx, "b", out)
		synctest.Wait()
		cancel()
		synctest.Wait()
		if a := <-out; a.err != context.Canceled {
			t.Fatal(a.err)
		}
		enqueue(s, context.Background(), "c", out)
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if a := <-out; a.err == nil {
			t.Fatal("expired queue request executed")
		}
		hold()
		hold()
		if s.active != 0 || s.waiting != 0 || len(s.groups) != 0 || s.expired != 1 || s.cancelled != 1 {
			t.Fatalf("bad cleanup %v", s.stats())
		}
	})
}
func TestSearchBudgetScalesAndReservesDatabaseCapacity(t *testing.T) {
	small := baseSearchBudget(1, 128*MiB)
	medium := baseSearchBudget(2, 384*MiB)
	large := baseSearchBudget(8, 2048*MiB)
	if small.Workers != 2 || medium.Workers != 4 || large.Workers != 16 {
		t.Fatalf("unexpected budgets %v %v %v", small, medium, large)
	}
	if small.Queue >= medium.Queue || medium.Queue >= large.Queue {
		t.Fatal("queue did not scale")
	}
	t.Setenv("MAX_DATABASE_CONNECTIONS", "6")
	t.Setenv("MAX_ACTIVE_SEARCHES", "100")
	b := CurrentSearchBudget()
	if b.Workers != 2 {
		t.Fatal("searches consume DB control headroom")
	}
}

func TestSchedulerChargesSlowQueriesAndDoesNotStarve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 512)
		s.budget.QueueWait = time.Minute
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hold, _, _ := s.acquire(ctx, "block", "test")
		done := make(chan string, 400)
		for range 150 {
			for _, id := range []string{"slow", "fast"} {
				go func(id string) {
					r, _, err := s.acquire(ctx, id, "test")
					if err != nil {
						return
					}
					d := 10 * time.Millisecond
					if id == "slow" {
						d = 100 * time.Millisecond
					}
					time.Sleep(d)
					r()
					done <- id
				}(id)
			}
		}
		synctest.Wait()
		hold()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		counts := map[string]int{}
		for len(done) > 0 {
			counts[<-done]++
		}
		if counts["slow"] < 5 || counts["fast"] < 50 || counts["fast"] < counts["slow"]*5 {
			t.Fatalf("service-cost fairness failed: %v", counts)
		}
		cancel()
		time.Sleep(time.Second)
		synctest.Wait()
		if s.active != 0 || s.waiting != 0 || len(s.groups) != 0 {
			t.Fatal("cancelled load leaked")
		}
	})
}

func TestMoreCommunitiesThanWorkersAllProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(2, 256)
		done := make(chan string, 120)
		for i := range 30 {
			for range 4 {
				go func(id string) {
					r, _, err := s.acquire(context.Background(), id, "test")
					if err != nil {
						done <- "failed"
						return
					}
					time.Sleep(5 * time.Millisecond)
					r()
					done <- id
				}(fmt.Sprint(i))
			}
		}
		time.Sleep(time.Second)
		synctest.Wait()
		counts := map[string]int{}
		for len(done) > 0 {
			counts[<-done]++
		}
		if len(counts) != 30 || counts["failed"] != 0 || s.completed != 120 {
			t.Fatalf("starvation: %v", counts)
		}
		if len(s.groups) != 0 {
			t.Fatal("idle community state retained")
		}
	})
}

func TestQueueAccountsForLargePayloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 20)
		s.budget.QueueBytes = 100 * 1024
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hold, _, _ := s.acquire(ctx, "a", "test")
		done := make(chan error, 2)
		go func() { _, _, err := s.acquire(ctx, "a", "test", 30*1024); done <- err }()
		synctest.Wait()
		if s.waitingBytes != 92*1024 {
			t.Fatal("wrong retained-byte accounting")
		}
		if _, _, err := s.acquire(ctx, "a", "test", 30*1024); err == nil {
			t.Fatal("large payloads bypassed byte budget")
		}
		cancel()
		synctest.Wait()
		<-done
		hold()
		if s.waitingBytes != 0 {
			t.Fatal("bytes not reclaimed")
		}
	})
}

func TestSerialExpensiveClientCannotResetServiceDebt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		slow, _, _ := s.acquire(ctx, "slow", "test")
		out := make(chan admission, 4)
		enqueue(s, ctx, "fast", out)
		enqueue(s, ctx, "fast", out)
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		slow()
		synctest.Wait()
		first := <-out
		if first.id != "fast" {
			t.Fatal("fast work not dispatched")
		}
		enqueue(s, ctx, "slow", out)
		synctest.Wait()
		time.Sleep(10 * time.Millisecond)
		first.release()
		synctest.Wait()
		second := <-out
		if second.id != "fast" {
			t.Fatal("idle transition forgave expensive work")
		}
		cancel()
		second.release()
		synctest.Wait()
		for len(out) > 0 {
			a := <-out
			if a.release != nil {
				a.release()
			}
		}
	})
}

func TestIdleCostHistoryIsBounded(t *testing.T) {
	s := testScheduler(1, 4)
	for i := range 200 {
		r, _, err := s.acquire(context.Background(), fmt.Sprint(i), "test")
		if err != nil {
			t.Fatal(err)
		}
		r()
	}
	if len(s.groups) != 0 || len(s.history) > 64 || s.historyLRU.Len() > 64 {
		t.Fatal("idle history grew without bound")
	}
}

func TestReaderFloodDoesNotLockOutItsCommunityPeers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(2, 4)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hold1, _, _ := s.acquire(ctx, "doom", "flood")
		hold2, _, _ := s.acquire(ctx, "doom", "flood")
		out := make(chan admission, 8)
		enqueueReader := func(reader string) {
			go func() { r, _, e := s.acquire(ctx, "doom", reader); out <- admission{reader, r, e} }()
		}
		for range 4 {
			enqueueReader("flood")
		}
		synctest.Wait()
		enqueueReader("quiet")
		synctest.Wait()
		if a := <-out; a.err == nil || a.id != "flood" {
			t.Fatal("quiet reader could not enter full queue")
		}
		hold1()
		synctest.Wait()
		a := <-out
		if a.err != nil || a.id != "quiet" {
			t.Fatal("reader flood monopolized its community")
		}
		cancel()
		a.release()
		hold2()
		synctest.Wait()
		for len(out) > 0 {
			a := <-out
			if a.release != nil {
				a.release()
			}
		}
		if s.active != 0 || s.waiting != 0 || s.waitingBytes != 0 || len(s.groups) != 0 {
			t.Fatal("reader queue leaked")
		}
	})
}

func TestExtraReaderConnectionsCannotBuyCommunityShare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 512)
		s.budget.QueueWait = time.Minute
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hold, _, _ := s.acquire(ctx, "block", "operator")
		done := make(chan string, 400)
		for i := range 150 {
			for _, cid := range []string{"many-readers", "one-reader"} {
				rid := "single"
				if cid == "many-readers" {
					rid = fmt.Sprint(i)
				}
				go func() {
					r, _, e := s.acquire(ctx, cid, rid)
					if e != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
					r()
					done <- cid
				}()
			}
		}
		synctest.Wait()
		hold()
		time.Sleep(time.Second)
		synctest.Wait()
		counts := map[string]int{}
		for len(done) > 0 {
			counts[<-done]++
		}
		if counts["many-readers"] < 35 || counts["one-reader"] < 35 {
			t.Fatalf("community share depends on connection count: %v", counts)
		}
		cancel()
		time.Sleep(time.Second)
		synctest.Wait()
		if len(s.groups) != 0 || s.waitingBytes != 0 {
			t.Fatal("nested state leaked")
		}
	})
}

func TestReaderCostFairnessSurvivesIdleTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(1, 20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		slow, _, _ := s.acquire(ctx, "doom", "slow")
		out := make(chan admission, 4)
		enqueueReader := func(reader string) {
			go func() { r, _, e := s.acquire(ctx, "doom", reader); out <- admission{reader, r, e} }()
		}
		enqueueReader("fast")
		enqueueReader("fast")
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		slow()
		synctest.Wait()
		first := <-out
		if first.id != "fast" {
			t.Fatal("fast reader did not run")
		}
		enqueueReader("slow")
		synctest.Wait()
		time.Sleep(10 * time.Millisecond)
		first.release()
		synctest.Wait()
		second := <-out
		if second.id != "fast" {
			t.Fatal("reader erased service debt by idling")
		}
		cancel()
		second.release()
		synctest.Wait()
		for len(out) > 0 {
			a := <-out
			if a.release != nil {
				a.release()
			}
		}
	})
}
