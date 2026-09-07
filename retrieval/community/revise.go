package community

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// Revise creates an explicit new version while preserving the original receipt.
// Uncertain/in-flight transfers must be resolved with their existing key first.
func Revise(ctx context.Context, root, id string) (Draft, error) {
	d, err := ReadDraft(root, id)
	if err != nil {
		return d, err
	}
	if d.ReplacedBy != "" {
		return ReadDraft(root, d.ReplacedBy)
	}
	if d.State == "draft" {
		return d, nil
	}
	if d.State == "queued" && d.LastStatus != 422 && d.LastStatus != 400 {
		return d, fmt.Errorf("resolve the queued transfer with its existing key before revising")
	}
	if d.State == "submitted" {
		client, e := NewClient(d.Connection.Service)
		if e != nil {
			return d, e
		}
		var sub Submission
		if e = client.Operation(ctx, "submission.get", d.Connection.CommunityID, map[string]string{"id": d.SubmissionID}, &sub); e != nil {
			return d, e
		}
		if sub.Digest != d.Digest {
			return d, fmt.Errorf("submission content does not match the saved draft")
		}
		if sub.State == "queued" || sub.State == "needs_review" {
			return d, fmt.Errorf("submission is still awaiting review; resolve it before creating another version")
		}
		if sub.State == "accepted" {
			db, e := publicationDB(root)
			if e != nil {
				return d, e
			}
			e = db.QueryRow(`SELECT finding_id,revision FROM receipts WHERE service=? AND community=? AND local_id=?`, d.Connection.Service, d.Connection.CommunityID, d.LocalID).Scan(&d.Document.FindingID, &d.Document.BaseRevision)
			db.Close()
			if e == sql.ErrNoRows {
				return d, fmt.Errorf("reconcile accepted receipts before revising this finding")
			}
			if e != nil {
				return d, e
			}
		}
	}
	old := d
	d.ID = uuid.NewString()
	d.State = "draft"
	d.SubmissionID = ""
	d.Digest = ""
	d.Error = ""
	d.LastStatus = 0
	if err = atomicJSON(draftPath(root, d.ID), d); err != nil {
		return d, err
	}
	old.ReplacedBy = d.ID
	old.State = "replaced"
	return d, atomicJSON(draftPath(root, old.ID), old)
}
