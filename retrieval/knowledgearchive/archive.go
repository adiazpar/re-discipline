// Package knowledgearchive defines a portable, knowledge-only archive. Each
// transport chunk is a Zstandard frame; concatenated frames form one JSONL file.
package knowledgearchive

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

const Format = "re-discipline-knowledge-v2"
const MaxRecord = 4 << 20
const MaxChunk = 6 << 20

type Record struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
type Summary struct {
	UncompressedBytes int64            `json:"uncompressed_bytes"`
	CommunityID       string           `json:"community_id"`
	Name              string           `json:"name"`
	Records           int64            `json:"records"`
	Counts            map[string]int64 `json:"counts"`
	SHA256            string           `json:"sha256"`
	Bytes             int64            `json:"bytes"`
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Chain(previous string, b []byte) string {
	h := sha256.New()
	h.Write([]byte(previous))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// Fields are allowlisted, never copied with SELECT * or object subtraction.
var Fields = map[string][]string{
	"manifest": {"format", "community_id", "name", "scope", "sequence"},
	"document": {"id", "path", "markdown", "fingerprint"},
	"end":      {"records", "chain"},
}

func Verify(path string) (Summary, error) {
	var out Summary
	out.Counts = map[string]int64{}
	f, e := os.Open(path)
	if e != nil {
		return out, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return out, e
	}
	out.Bytes = info.Size()
	h := sha256.New()
	decoder, e := zstd.NewReader(io.TeeReader(f, h), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(16<<20), zstd.WithDecoderMaxWindow(1<<20))
	if e != nil {
		return out, e
	}
	defer decoder.Close()
	scan := bufio.NewScanner(decoder)
	scan.Buffer(make([]byte, 64<<10), MaxRecord)
	first, ended := true, false
	for scan.Scan() {
		out.UncompressedBytes += int64(len(scan.Bytes()) + 1)
		var r Record
		if e = json.Unmarshal(scan.Bytes(), &r); e != nil {
			return out, e
		}
		fields, ok := Fields[r.Type]
		if !ok || ended {
			return out, fmt.Errorf("invalid archive record %q", r.Type)
		}
		var v map[string]json.RawMessage
		if e = json.Unmarshal(r.Data, &v); e != nil {
			return out, e
		}
		allowed := map[string]bool{}
		for _, key := range fields {
			allowed[key] = true
			if _, present := v[key]; !present {
				return out, fmt.Errorf("missing archive field %s.%s", r.Type, key)
			}
		}
		for key := range v {
			if !allowed[key] {
				return out, fmt.Errorf("unexpected archive field %s.%s", r.Type, key)
			}
		}
		if first {
			var m struct{ Format, CommunityID, Name string }
			var id string
			json.Unmarshal(v["format"], &m.Format)
			json.Unmarshal(v["community_id"], &id)
			json.Unmarshal(v["name"], &m.Name)
			if r.Type != "manifest" || m.Format != Format || id == "" {
				return out, fmt.Errorf("invalid archive manifest")
			}
			out.CommunityID, out.Name = id, m.Name
			first = false
			continue
		}
		if r.Type == "manifest" {
			return out, fmt.Errorf("duplicate archive manifest")
		}
		if r.Type == "end" {
			var n int64
			if json.Unmarshal(v["records"], &n) != nil || n != out.Records {
				return out, fmt.Errorf("archive record count mismatch")
			}
			ended = true
			continue
		}
		out.Records++
		out.Counts[r.Type]++
	}
	if e = scan.Err(); e != nil {
		return out, e
	}
	if first || !ended {
		return out, fmt.Errorf("archive is incomplete")
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
