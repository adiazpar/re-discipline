package server

import (
	"context"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func (s *Server) admitStats(ctx context.Context) (func(), error) {
	select {
	case s.statsSlots <- struct{}{}:
		return func() { <-s.statsSlots }, nil
	default:
	}
	select {
	case s.statsWaiters <- struct{}{}:
		defer func() { <-s.statsWaiters }()
	default:
		return nil, fail(503, "statistics busy; use the last summary and retry later")
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case s.statsSlots <- struct{}{}:
		return func() { <-s.statsSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fail(503, "statistics busy; use the last summary and retry later")
	}
}

func (s *Server) communityStats(ctx context.Context, p principal, cid string) (kb.CommunityStats, error) {
	out := p.stats
	if p.operator {
		if err := validUUID(cid); err != nil {
			return out, err
		}
		if err := s.DB.QueryRow(ctx, `SELECT knowledge_count,knowledge_bytes FROM community.libraries WHERE id=$1 AND deletion_id IS NULL`, cid).Scan(&out.Documents, &out.KnowledgeBytes); err != nil {
			return out, err
		}
	}
	out.CommunityID = cid
	out.ObservedAt = time.Now().UTC()
	return out, nil
}
