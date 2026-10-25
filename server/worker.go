package server

import (
	"context"
	"time"
)

func (s *Server) RunWorker(ctx context.Context) {
	go s.runIndexWorker(ctx)
	go s.runConnectionMaintenance(ctx)
	go s.runDeletionCleanup(ctx)
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_, _ = s.DB.Exec(ctx, `DELETE FROM community.upload_receipts WHERE (community_id,key) IN (SELECT community_id,key FROM community.upload_receipts WHERE created_at<now()-interval '1 day' AND community_id IN(SELECT id FROM community.libraries WHERE deletion_id IS NULL) LIMIT 10000)`)
		}
	}
}
