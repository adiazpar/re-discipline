package server

import (
	"context"
	"golang.org/x/sync/semaphore"
	"sync"
)

const indexWriterWeight int64 = 1 << 30

type searchState struct {
	index *semaphore.Weighted
	refs  int
	work  map[*communityWork]struct{}
}

type communityWork struct{ cancel context.CancelCauseFunc }

func (s *Server) searchState(cid string) *searchState {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	state := s.searchStates[cid]
	if state == nil {
		state = &searchState{index: semaphore.NewWeighted(indexWriterWeight), work: map[*communityWork]struct{}{}}
		s.searchStates[cid] = state
	}
	state.refs++
	return state
}

func (s *Server) releaseSearchState(cid string, state *searchState) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	state.refs--
	if state.refs == 0 {
		delete(s.searchStates, cid)
	}
}

// Keep cancellable requests and background work registered across SQLite/DB
// reads and optional model calls. State disappears after its final user exits.
func (s *Server) trackCommunityWork(ctx context.Context, cid string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	state := s.searchState(cid)
	w := &communityWork{cancel}
	s.searchMu.Lock()
	state.work[w] = struct{}{}
	s.searchMu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			s.searchMu.Lock()
			delete(state.work, w)
			s.searchMu.Unlock()
			s.releaseSearchState(cid, state)
		})
	}
}

func (s *Server) cancelCommunityWork(cid string) { s.cancelCommunityWorkWith(cid, errCommunityDeleted) }
func (s *Server) cancelCommunityWorkWith(cid string, cause error) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if state := s.searchStates[cid]; state != nil {
		for w := range state.work {
			w.cancel(cause)
		}
	}
}
func (s *Server) lockIndex(ctx context.Context, cid string, write bool) (func(), error) {
	weight := int64(1)
	if write {
		weight = indexWriterWeight
	}
	state := s.searchState(cid)
	lock := state.index
	if err := lock.Acquire(ctx, weight); err != nil {
		s.releaseSearchState(cid, state)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { lock.Release(weight); s.releaseSearchState(cid, state) }) }, nil
}
