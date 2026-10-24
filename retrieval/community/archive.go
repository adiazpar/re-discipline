package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	ka "github.com/adiazpar/re-discipline/retrieval/knowledgearchive"
	"github.com/gofrs/flock"
	"github.com/klauspost/compress/zstd"
)

type archiveSliceKey struct{}

type archiveCheckpoint struct {
	Service   string `json:"service"`
	Community string `json:"community"`
	Ticket    string `json:"ticket_hash"`
	Index     int64  `json:"index"`
	Offset    int64  `json:"offset"`
	Chain     string `json:"chain"`
	Records   int64  `json:"records"`
	SHA256    string `json:"sha256"`
	Done      bool   `json:"done"`
}

// DownloadArchive never locks or deletes a community itself. Those steps need
// separate, explicit user decisions. Data goes to disk, not the MCP/model output.
func (c *Client) DownloadArchive(ctx context.Context, cid, ticket, path string) (any, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("community.archive requires server operator credentials")
	}
	if len(ticket) < 32 {
		return nil, fmt.Errorf("provide the prepared deletion ticket")
	}
	var status struct {
		SessionID string `json:"session_id"`
		Phase     string `json:"phase"`
		Archive   bool   `json:"archive"`
	}
	if e := c.Operation(ctx, "community.delete", cid, map[string]any{"step": "status"}, &status); e != nil {
		return nil, e
	}
	if !status.Archive || (status.Phase != "locked" && status.Phase != "downloaded" && status.Phase != "verified") {
		return nil, fmt.Errorf("first obtain user approval and lock this community with archive:true")
	}
	if path == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		path = filepath.Join(home, ".re-discipline", "archives", cid, status.SessionID+".re-knowledge.jsonl.zst")
	}
	path, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	lock := flock.New(path + ".lock")
	ok, e := lock.TryLockContext(ctx, 100*time.Millisecond)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, fmt.Errorf("archive download is already active for this file")
	}
	defer lock.Unlock()
	checkpointPath, partial := path+".state.json", path+".partial"
	state := archiveCheckpoint{Service: c.URL, Community: cid, Ticket: ka.Digest([]byte(ticket)), SHA256: ka.Digest(nil)}
	saved, e := os.ReadFile(checkpointPath)
	if e == nil {
		if e = json.Unmarshal(saved, &state); e != nil {
			return nil, e
		}
		if state.Service != c.URL || state.Community != cid || state.Ticket != ka.Digest([]byte(ticket)) || state.Offset < 0 || state.Index < 0 {
			return nil, fmt.Errorf("archive destination belongs to another session")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("archive destination must be a regular file")
		}
		if !state.Done {
			return nil, fmt.Errorf("archive destination already exists; choose a new path")
		}
		return c.finishArchive(ctx, cid, ticket, path, state, checkpointPath)
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	// Refuse symlinks and unexplained partial files; never follow them into local research.
	if info, e := os.Lstat(partial); e == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || saved == nil {
			return nil, fmt.Errorf("unsafe or unrelated partial archive file")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	flags := os.O_RDWR | os.O_CREATE
	if saved == nil {
		flags |= os.O_EXCL
	}
	f, e := os.OpenFile(partial, flags, 0600)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size() < state.Offset {
		return nil, fmt.Errorf("partial archive is shorter than its checkpoint")
	}
	h := sha256.New()
	if _, e = io.CopyN(h, f, state.Offset); e != nil {
		return nil, e
	}
	if hex.EncodeToString(h.Sum(nil)) != state.SHA256 {
		return nil, fmt.Errorf("partial archive checksum mismatch; choose a new path and cancel/restart this export")
	}
	if e = f.Truncate(state.Offset); e != nil {
		return nil, e
	}
	if saved == nil {
		if e = atomicJSON(checkpointPath, state); e != nil {
			return nil, e
		}
	}
	decoder, e := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(16<<20), zstd.WithDecoderMaxWindow(1<<20))
	if e != nil {
		return nil, e
	}
	defer decoder.Close()
	for !state.Done {
		compressed, header, e := c.downloadArchiveChunk(ctx, cid, ticket, state.Index)
		if e != nil {
			return nil, fmt.Errorf("archive incomplete; resume at the same path (community remains intact): %w", e)
		}
		plain, e := decoder.DecodeAll(compressed, nil)
		if e != nil {
			return nil, e
		}
		if len(plain) > ka.MaxChunk || ka.Digest(compressed) != header.Get("X-Archive-SHA256") {
			return nil, fmt.Errorf("archive chunk checksum or size mismatch")
		}
		next, e := strconv.ParseInt(header.Get("X-Archive-Next"), 10, 64)
		if e != nil || next != state.Index+1 {
			return nil, fmt.Errorf("archive chunk sequence mismatch")
		}
		chain := ka.Chain(state.Chain, plain)
		if chain != header.Get("X-Archive-Chain") {
			return nil, fmt.Errorf("archive chain mismatch")
		}
		records, e := strconv.ParseInt(header.Get("X-Archive-Records"), 10, 64)
		if e != nil || records < state.Records {
			return nil, fmt.Errorf("invalid archive record count")
		}
		done, e := strconv.ParseBool(header.Get("X-Archive-Done"))
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(compressed); e != nil {
			return nil, e
		}
		h.Write(compressed)
		if e = f.Sync(); e != nil {
			return nil, e
		}
		state.Index, state.Offset, state.Chain, state.Records, state.Done = next, state.Offset+int64(len(compressed)), chain, records, done
		state.SHA256 = hex.EncodeToString(h.Sum(nil))
		if e = atomicJSON(checkpointPath, state); e != nil {
			return nil, e
		}
		if until, ok := ctx.Value(archiveSliceKey{}).(time.Time); ok && !state.Done && time.Now().After(until) {
			return map[string]any{"path": path, "phase": "downloading", "verified": false, "deleted": false, "resume_required": true, "bytes_downloaded": state.Offset, "records_downloaded": state.Records}, nil
		}
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	summary, e := ka.Verify(partial)
	if e != nil {
		return nil, e
	}
	if summary.CommunityID != cid || summary.Records != state.Records || summary.SHA256 != state.SHA256 {
		return nil, fmt.Errorf("completed archive verification failed")
	}
	// Link is atomic and refuses to overwrite an existing destination. Keep the
	// verified partial on filesystems lacking this operation, rather than overwrite.
	if e = os.Link(partial, path); e != nil {
		return nil, e
	}
	if e = os.Remove(partial); e != nil {
		return nil, e
	}
	return c.finishArchive(ctx, cid, ticket, path, state, checkpointPath)
}

func (c *Client) finishArchive(ctx context.Context, cid, ticket, path string, state archiveCheckpoint, checkpointPath string) (any, error) {
	summary, e := ka.Verify(path)
	if e != nil {
		return nil, e
	}
	if summary.CommunityID != cid || summary.Records != state.Records || summary.SHA256 != state.SHA256 {
		return nil, fmt.Errorf("saved archive does not match this export")
	}
	var result map[string]any
	if e = c.Operation(ctx, "community.delete", cid, map[string]any{"step": "verify", "ticket": ticket, "chain": state.Chain, "records": state.Records}, &result); e != nil {
		return nil, fmt.Errorf("archive saved and verified at %s; server acknowledgement pending, resume at this path: %w", path, e)
	}
	// Preserve the checkpoint for safe lost-response retries; it contains no
	// operator credential and is outside the archive. Final deletion removes none
	// of the owner's archive files. Files use owner-only permissions.
	return map[string]any{"path": path, "verified": true, "summary": summary, "phase": "verified", "confirmation_required": true, "deleted": false}, nil
}

func (c *Client) downloadArchiveChunk(ctx context.Context, cid, ticket string, index int64) ([]byte, http.Header, error) {
	body, _ := json.Marshal(Request{Action: "community.archive.chunk", Community: cid, Data: json.RawMessage(rawArchive(map[string]any{"ticket": ticket, "index": index}))})
	for attempt := 0; attempt < 4; attempt++ {
		req, e := http.NewRequestWithContext(ctx, "POST", c.URL+"/v1/operations", bytes.NewReader(body))
		if e != nil {
			return nil, nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		res, e := c.HTTP.Do(req)
		if e != nil {
			return nil, nil, e
		}
		b, e := io.ReadAll(io.LimitReader(res.Body, ka.MaxChunk+65537))
		res.Body.Close()
		if e != nil {
			return nil, nil, e
		}
		if len(b) > ka.MaxChunk+65536 {
			return nil, nil, fmt.Errorf("archive response exceeds bound")
		}
		if res.StatusCode == 200 {
			return b, res.Header, nil
		}
		if res.StatusCode == 503 && attempt < 3 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		var v struct {
			Error string `json:"error"`
		}
		json.Unmarshal(b, &v)
		return nil, nil, &APIError{Status: res.StatusCode, Message: v.Error, RetryAfter: res.Header.Get("Retry-After")}
	}
	return nil, nil, fmt.Errorf("archive busy; resume later")
}
func rawArchive(v any) []byte { b, _ := json.Marshal(v); return b }
