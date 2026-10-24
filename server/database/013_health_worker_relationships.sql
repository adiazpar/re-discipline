-- Health compares reviewed relationships but cannot create or change them.
GRANT SELECT ON community.finding_relations TO re_community_worker;
CREATE POLICY relations_worker_read ON community.finding_relations
 FOR SELECT TO re_community_worker USING(true);
