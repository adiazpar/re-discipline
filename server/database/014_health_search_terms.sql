-- Ranking and index rechecks reuse the same parsed text as ingestion.
-- Generated columns also clear automatically when a document is erased.
ALTER TABLE community.revisions ADD COLUMN search_terms tsvector
 GENERATED ALWAYS AS (to_tsvector('english',document->>'markdown')) STORED;
ALTER TABLE community.submissions ADD COLUMN search_terms tsvector
 GENERATED ALWAYS AS (to_tsvector('english',document->>'markdown')) STORED;
CREATE INDEX revisions_health_terms ON community.revisions USING gin(search_terms) WHERE NOT deleted;
CREATE INDEX submissions_health_terms ON community.submissions USING gin(search_terms)
 WHERE state IN ('queued','needs_review','changes_requested','conflict');
CREATE INDEX health_work_visit_order ON community.health_work
 ((COALESCE(last_analyzed,'epoch'::timestamptz)),available_at,community_id,subject_id);
