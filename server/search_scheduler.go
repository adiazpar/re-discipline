package server

import (
	"container/heap"
	"container/list"
	"context"
	"sync"
	"time"
)

// Two-level cost-aware queue: communities, then verified reader connections.
// Each reader has a FIFO. Virtual service is charged at both levels at dispatch
// and corrected to observed slot time. Extra readers cannot buy community share.
// This approximates occupied-worker fairness, not per-tenant CPU accounting.
// Running work is never preempted; idle capacity is always lendable.
type searchScheduler struct {
	mu                                      sync.Mutex
	budget                                  SearchBudget
	groups                                  map[string]*searchGroup
	ready                                   groupHeap
	history                                 map[historyKey]*list.Element
	historyLRU                              list.List
	active, waiting                         int
	waitingBytes                            int64
	clock, order                            uint64
	completed, rejected, expired, cancelled uint64
	waitTotal, runTotal                     time.Duration
}
type serviceHistory struct {
	key      historyKey
	service  uint64
	estimate time.Duration
	clock    uint64
	at       time.Time
}
type historyKey struct{ community, reader string }

type searchGroup struct {
	discardHistory bool
	id             string
	queue          list.List
	active         int
	service        uint64
	estimate       time.Duration
	order          uint64
	heapIndex      int
	parent         *searchGroup
	readers        map[string]*searchGroup
	ready          groupHeap
	waiting        int
	clock          uint64
}
type searchWaiter struct {
	group             *searchGroup
	reader            *searchGroup
	element           *list.Element
	ready             chan struct{}
	ctx               context.Context
	deadline          time.Time
	enqueued, started time.Time
	charged           time.Duration
	readerCharged     time.Duration
	err               error
	running           bool
	bytes             int64
}
type groupHeap []*searchGroup

func (h groupHeap) Len() int { return len(h) }
func (h groupHeap) Less(i, j int) bool {
	if h[i].active != h[j].active {
		return h[i].active < h[j].active
	}
	if h[i].service == h[j].service {
		return h[i].order < h[j].order
	}
	return h[i].service < h[j].service
}
func (h groupHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].heapIndex = i; h[j].heapIndex = j }
func (h *groupHeap) Push(x any)   { g := x.(*searchGroup); g.heapIndex = len(*h); *h = append(*h, g) }
func (h *groupHeap) Pop() any {
	old := *h
	g := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	g.heapIndex = -1
	return g
}
func newSearchScheduler(b SearchBudget) *searchScheduler {
	return &searchScheduler{budget: b, groups: map[string]*searchGroup{}, history: map[historyKey]*list.Element{}}
}

func (s *searchScheduler) acquire(ctx context.Context, cid, readerID string, payloadSize ...int) (func(), time.Duration, error) {
	if cid == "" || readerID == "" {
		return nil, 0, fail(400, "verified community and reader identity required")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	now := time.Now()
	bytes := int64(32 * 1024)
	if len(payloadSize) > 0 {
		bytes += 2 * int64(max(0, payloadSize[0]))
	}
	if bytes > s.budget.QueueBytes {
		return nil, 0, fail(503, "retrieval request exceeds queue memory budget")
	}
	w := &searchWaiter{ctx: ctx, ready: make(chan struct{}), enqueued: now, deadline: now.Add(s.budget.QueueWait), bytes: bytes}
	s.mu.Lock()
	g := s.groups[cid]
	if g == nil {
		g = s.restore(historyKey{community: cid}, s.clock, now)
		g.readers = map[string]*searchGroup{}
	}
	reader := g.readers[readerID]
	if reader == nil {
		reader = s.restore(historyKey{cid, readerID}, g.clock, now)
		reader.parent = g
	}
	for s.waiting >= s.budget.Queue || s.waitingBytes+bytes > s.budget.QueueBytes {
		// First protect community admission, then readers within this community.
		// Only a strictly longer queue donates space; balanced queues reject.
		var largest *searchGroup
		for _, other := range s.groups {
			if other.waiting > g.waiting+1 && (largest == nil || other.waiting > largest.waiting) {
				largest = other
			}
		}
		var donor *searchGroup
		if largest != nil {
			donor = largest.longestReader(-1)
		} else {
			donor = g.longestReader(reader.queue.Len() + 1)
		}
		if donor == nil {
			s.rejected++
			s.mu.Unlock()
			return nil, 0, fail(503, "search queue full; retry shortly")
		}
		victim := donor.queue.Back().Value.(*searchWaiter)
		s.remove(victim)
		victim.err = fail(503, "search queue busy; retry shortly")
		s.rejected++
		close(victim.ready)
	}
	if s.groups[cid] == nil {
		s.forget(historyKey{community: cid})
		s.groups[cid] = g
	}
	if g.readers[readerID] == nil {
		s.forget(historyKey{cid, readerID})
		g.readers[readerID] = reader
	}
	w.group = g
	w.reader = reader
	w.element = reader.queue.PushBack(w)
	g.waiting++
	s.waiting++
	s.waitingBytes += w.bytes
	if reader.heapIndex < 0 {
		s.order++
		reader.order = s.order
		heap.Push(&g.ready, reader)
	}
	if g.heapIndex < 0 {
		s.order++
		g.order = s.order
		heap.Push(&s.ready, g)
	}
	s.dispatch()
	s.mu.Unlock()
	timer := time.NewTimer(time.Until(w.deadline))
	defer timer.Stop()
	select {
	case <-w.ready:
	case <-ctx.Done():
	case <-timer.C:
	}
	s.mu.Lock()
	if !w.running && w.err == nil {
		s.remove(w)
		if ctx.Err() != nil {
			w.err = ctx.Err()
			s.cancelled++
		} else {
			w.err = fail(503, "search queue wait expired; retry shortly")
			s.expired++
		}
	}
	err := w.err
	running := w.running
	wait := time.Since(w.enqueued)
	if running {
		wait = w.started.Sub(w.enqueued)
	}
	s.mu.Unlock()
	if !running {
		return nil, wait, err
	}
	var once sync.Once
	release := func() { once.Do(func() { s.finish(w) }) }
	// A cancellation racing with dispatch must still return the occupied slot.
	if ctx.Err() != nil {
		release()
		return nil, wait, ctx.Err()
	}
	return release, wait, nil
}

// remove is called only with mu held, for a queued waiter.
func (s *searchScheduler) remove(w *searchWaiter) {
	g, reader := w.group, w.reader
	if w.element == nil {
		return
	}
	reader.queue.Remove(w.element)
	w.element = nil
	g.waiting--
	s.waiting--
	s.waitingBytes -= w.bytes
	if reader.queue.Len() == 0 && reader.heapIndex >= 0 {
		heap.Remove(&g.ready, reader.heapIndex)
	}
	if g.waiting == 0 && g.heapIndex >= 0 {
		heap.Remove(&s.ready, g.heapIndex)
	}
	if reader.active == 0 && reader.queue.Len() == 0 {
		s.retire(reader)
	}
	if g.active == 0 && g.waiting == 0 {
		s.retire(g)
	}
}
func (s *searchScheduler) dispatch() {
	for s.active < s.budget.Workers && s.ready.Len() > 0 {
		g := heap.Pop(&s.ready).(*searchGroup)
		reader := heap.Pop(&g.ready).(*searchGroup)
		w := reader.queue.Front().Value.(*searchWaiter)
		reader.queue.Remove(w.element)
		w.element = nil
		g.waiting--
		s.waiting--
		s.waitingBytes -= w.bytes
		now := time.Now()
		if w.ctx.Err() != nil {
			w.err = w.ctx.Err()
			s.cancelled++
		} else if !now.Before(w.deadline) {
			w.err = fail(503, "search queue wait expired; retry shortly")
			s.expired++
		}
		if w.err == nil {
			s.clock = max(s.clock, g.service)
			g.clock = max(g.clock, reader.service)
			w.charged = g.estimate
			w.readerCharged = reader.estimate
			g.service += uint64(w.charged)
			reader.service += uint64(w.readerCharged)
			g.active++
			reader.active++
			s.active++
			w.running = true
			w.started = now
			s.waitTotal += now.Sub(w.enqueued)
		}
		close(w.ready)
		if reader.queue.Len() > 0 {
			s.order++
			reader.order = s.order
			heap.Push(&g.ready, reader)
		} else if reader.active == 0 {
			s.retire(reader)
		}
		if g.waiting > 0 {
			s.order++
			g.order = s.order
			heap.Push(&s.ready, g)
		} else if g.active == 0 {
			s.retire(g)
		}
	}
}
func (s *searchScheduler) finish(w *searchWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, reader := w.group, w.reader
	duration := max(time.Millisecond, time.Since(w.started))
	// Reconcile estimated and observed work without unsigned underflow or an
	// idle-credit bank. Slow queries pay more turns than inexpensive queries.
	adjusted := int64(g.service) + int64(duration-w.charged)
	g.service = max(s.clock, uint64(max(0, adjusted)))
	g.estimate = (g.estimate*3 + duration) / 4
	adjusted = int64(reader.service) + int64(duration-w.readerCharged)
	reader.service = max(g.clock, uint64(max(0, adjusted)))
	reader.estimate = (reader.estimate*3 + duration) / 4
	reader.active--
	g.active--
	s.active--
	s.completed++
	s.runTotal += duration
	if reader.heapIndex >= 0 {
		heap.Fix(&g.ready, reader.heapIndex)
	}
	if g.heapIndex >= 0 {
		heap.Fix(&s.ready, g.heapIndex)
	}
	if reader.active == 0 && reader.queue.Len() == 0 {
		s.retire(reader)
	}
	if g.active == 0 && g.waiting == 0 {
		s.retire(g)
	}
	s.dispatch()
}

// Retain bounded, short-lived service debt, separately from live queues.
// A serial expensive client must not reset its cost by briefly going idle.
func (s *searchScheduler) retire(g *searchGroup) {
	key := historyKey{community: g.id}
	if g.parent == nil {
		delete(s.groups, g.id)
	} else {
		key = historyKey{g.parent.id, g.id}
		delete(g.parent.readers, g.id)
	}
	s.forget(key)
	if g.discardHistory || (g.parent != nil && g.parent.discardHistory) {
		return
	}
	s.history[key] = s.historyLRU.PushFront(serviceHistory{key, g.service, g.estimate, g.clock, time.Now()})
	// One shared bound, not a separate history allowance per community.
	for s.historyLRU.Len() > max(64, min(4096, s.budget.Queue*2)) {
		e := s.historyLRU.Back()
		delete(s.history, e.Value.(serviceHistory).key)
		s.historyLRU.Remove(e)
	}
}

// Deletion releases queued work immediately. Running operations are cancelled
// through their registered contexts and release their occupied slots normally.
func (s *searchScheduler) dropCommunity(cid string) { s.dropCommunityWith(cid, errCommunityDeleted) }
func (s *searchScheduler) dropCommunityWith(cid string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.groups[cid]; g != nil {
		g.discardHistory = true
		for _, reader := range g.readers {
			for reader.queue.Len() > 0 {
				w := reader.queue.Front().Value.(*searchWaiter)
				w.err = cause
				s.remove(w)
				s.cancelled++
				close(w.ready)
			}
		}
	}
	for key := range s.history {
		if key.community == cid {
			s.forget(key)
		}
	}
	s.dispatch()
}
func (s *searchScheduler) forget(key historyKey) {
	if e := s.history[key]; e != nil {
		delete(s.history, key)
		s.historyLRU.Remove(e)
	}
}
func (s *searchScheduler) restore(key historyKey, clock uint64, now time.Time) *searchGroup {
	id := key.reader
	if id == "" {
		id = key.community
	}
	g := &searchGroup{id: id, service: clock, estimate: 50 * time.Millisecond, heapIndex: -1}
	if e := s.history[key]; e != nil {
		h := e.Value.(serviceHistory)
		if now.Sub(h.at) < time.Minute {
			g.service, g.estimate, g.clock = max(clock, h.service), h.estimate, h.clock
		}
	}
	return g
}
func (g *searchGroup) longestReader(minimum int) *searchGroup {
	var largest *searchGroup
	for _, reader := range g.readers {
		if reader.queue.Len() > max(0, minimum) && (largest == nil || reader.queue.Len() > largest.queue.Len()) {
			largest = reader
		}
	}
	return largest
}
func (s *searchScheduler) stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := map[string]any{}
	for id, g := range s.groups {
		groups[id] = map[string]any{"running": g.active, "queued": g.waiting, "active_connections": len(g.readers), "estimated_search_ms": g.estimate.Milliseconds()}
	}
	return map[string]any{"budget": s.budget, "running": s.active, "queued": s.waiting, "queued_bytes": s.waitingBytes, "communities": groups, "completed": s.completed, "rejected": s.rejected, "queue_expired": s.expired, "cancelled": s.cancelled, "total_wait_ms": s.waitTotal.Milliseconds(), "total_run_ms": s.runTotal.Milliseconds()}
}
