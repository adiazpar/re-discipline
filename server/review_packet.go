package server

import (
	"context"
	"strconv"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

type resolutionPlan struct {
	Mode         string `json:"mode"`
	FindingID    string `json:"finding_id,omitempty"`
	BaseRevision string `json:"base_revision,omitempty"`
	Effect       string `json:"effect"`
}
type comparison struct {
	Reference    claimRef       `json:"reference"`
	Claim        semantic.Claim `json:"claim"`
	Relationship string         `json:"relationship"`
	Reason       string         `json:"reason"`
}
type reviewPacket struct {
	Submission  kb.Submission  `json:"submission"`
	Comparisons []comparison   `json:"comparisons"`
	Limited     bool           `json:"limited"`
	Token       string         `json:"token"`
	Plan        resolutionPlan `json:"plan"`
}

func (s *Server) packet(ctx context.Context, tx pgx.Tx, l kb.Library, v kb.Submission) (reviewPacket, error) {
	out := reviewPacket{Submission: v, Comparisons: []comparison{}, Plan: resolutionPlan{Mode: "create", Effect: "Publish a separate claim, retaining this contributor's authorship."}}
	source := healthClaim{Ref: claimRef{Kind: "submission", ID: v.ID, Version: v.Digest + "/" + strconv.Itoa(v.ResolutionVersion)}, Document: v.Document, Status: v.State}
	candidates, limited, err := healthCandidates(ctx, tx, l.ID, source)
	if err != nil {
		return out, err
	}
	out.Limited = limited
	refs := []claimRef{}
	for i, other := range candidates {
		refs = append(refs, other.Ref)
		item := comparison{Reference: other.Ref, Claim: kb.ProjectClaim(other.Document, other.Status), Relationship: "unknown", Reason: "Compare the asserted information and all applicability conditions."}
		if kb.ClaimDigest(v.Document) == kb.ClaimDigest(other.Document) && semantic.KnownScope(v.Document.Build) {
			item.Relationship = "equivalent"
			item.Reason = "Identical claim and applicability; retain every contributor in one reader entry."
		} else {
			// Only use durable, current background judgments. Opening review never waits
			// for inference, and RLS still governs every pending comparison above.
			var kind, reason string
			e := tx.QueryRow(ctx, `SELECT kind,reason FROM community.health_issues WHERE community_id=$1 AND policy_version=$2 AND state='open' AND ((subject_kind=$3 AND subject_id::text=$4 AND subject_version=$5 AND other_kind=$6 AND other_id=$7 AND other_version=$8) OR (subject_kind=$6 AND subject_id::text=$7 AND subject_version=$8 AND other_kind=$3 AND other_id=$4 AND other_version=$5)) ORDER BY created_at DESC LIMIT 1`, l.ID, l.PolicyVersion, source.Ref.Kind, source.Ref.ID, source.Ref.Version, other.Ref.Kind, other.Ref.ID, other.Ref.Version).Scan(&kind, &reason)
			if e == nil {
				item.Relationship = kind
				item.Reason = reason
			} else if e != pgx.ErrNoRows {
				return out, e
			}
		}
		if (item.Relationship == "equivalent" || item.Relationship == "duplicate") && other.Ref.Kind == "finding" && other.Status == "active" && out.Plan.Mode == "create" {
			target, e := canonicalRef(ctx, tx, l.ID, other.Ref)
			if e != nil {
				return out, e
			}
			out.Plan = resolutionPlan{Mode: "contribution", FindingID: target.ID, BaseRevision: target.Version, Effect: "Add this contributor's claim and credit to the existing reader entry."}
		}
		// Compact preview; exact versions remain fetchable for a complete comparison.
		maxBody := 1800
		if i >= 8 {
			maxBody = 240
		}
		body := []rune(item.Claim.Body)
		if len(body) > maxBody {
			item.Claim.Body = string(body[:maxBody])
			item.Claim.Truncated = true
		}
		out.Comparisons = append(out.Comparisons, item)
	}
	if v.Document.FindingID != "" {
		out.Plan = resolutionPlan{Mode: "revision", FindingID: v.Document.FindingID, BaseRevision: v.Document.BaseRevision, Effect: "Apply this correction to the existing claim."}
	}
	if !v.IdentityReview && v.ResolutionVersion > 0 && v.FindingID != "" && v.BaseRevision != "" && v.Document.FindingID == "" {
		out.Plan = resolutionPlan{Mode: "revision", FindingID: v.FindingID, BaseRevision: v.BaseRevision, Effect: "Apply the reviewed revision to the existing claim."}
	}
	if v.ContributionTarget != "" {
		out.Plan = resolutionPlan{Mode: "contribution", FindingID: v.ContributionTarget, BaseRevision: v.ContributionRevision, Effect: "Add this contributor's claim and credit to the existing reader entry."}
	}
	if out.Plan.FindingID != "" {
		found := false
		for _, item := range out.Comparisons {
			if item.Reference.Kind == "finding" && item.Reference.ID == out.Plan.FindingID {
				found = true
				break
			}
		}
		if !found {
			target, e := loadHealthClaim(ctx, tx, l.ID, claimRef{Kind: "finding", ID: out.Plan.FindingID})
			if e != nil {
				return out, e
			}
			claim := kb.ProjectClaim(target.Document, target.Status)
			if body := []rune(claim.Body); len(body) > 1800 {
				claim.Body, claim.Truncated = string(body[:1800]), true
			}
			out.Comparisons = append([]comparison{{Reference: target.Ref, Claim: claim, Relationship: "publication_target", Reason: "This is the reader entry affected by the proposed acceptance. Compare its full applicability and content."}}, out.Comparisons...)
			refs = append(refs, target.Ref)
		}
	}
	out.Token = semantic.Hash([]any{l.ID, l.PolicyVersion, v.ID, v.Digest, v.ResolutionVersion, refs, limited, out.Plan})
	return out, nil
}
