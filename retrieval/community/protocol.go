// Package community implements the community wire format and local client.
package community

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const ProtocolVersion = 1

type Request struct {
	Action    string          `json:"action"`
	Community string          `json:"community,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type Policy struct {
	Scope      string   `json:"scope"`
	Exclusions []string `json:"exclusions"`
	Assistance bool     `json:"assistance,omitempty"` // optional query ranking
}

type Library struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Visibility    string `json:"visibility"`
	Policy        Policy `json:"policy"`
	PolicyVersion int    `json:"policy_version"`
	Sequence      int64  `json:"sequence"`
	Role          string `json:"role,omitempty"`
}

type Document struct {
	SourcePath string `json:"source_path"`
	Markdown   string `json:"markdown"`
	Kind       string `json:"kind"`
	Grade      string `json:"grade"`
	Build      string `json:"build"`
}

func ValidateLibrary(l Library) error {
	if len(strings.TrimSpace(l.Name)) < 2 || len(l.Name) > 120 {
		return fmt.Errorf("name must contain 2 to 120 characters")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}$`).MatchString(l.Slug) {
		return fmt.Errorf("slug must contain 3 to 63 lowercase letters, digits, or hyphens")
	}
	if l.Visibility != "public" && l.Visibility != "private" {
		return fmt.Errorf("visibility must be public or private")
	}
	if len(strings.TrimSpace(l.Policy.Scope)) < 10 || len(l.Policy.Scope) > 8000 {
		return fmt.Errorf("scope must contain 10 to 8000 characters")
	}
	return nil
}
