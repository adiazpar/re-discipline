package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func checkRecoveryHealth(dir string, now time.Time) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "auto-update-failed")); err == nil {
		return fmt.Errorf("automatic deployment requires operator attention")
	}
	for name, maxAge := range map[string]time.Duration{"backup": 28 * time.Hour, "restore": 9 * 24 * time.Hour} {
		if _, err := os.Stat(filepath.Join(dir, name+"-failed")); err == nil {
			return fmt.Errorf("%s requires operator attention", name)
		}
		b, err := os.ReadFile(filepath.Join(dir, name+"-last-success"))
		if err != nil {
			return fmt.Errorf("%s status unavailable", name)
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
		if err != nil || now.Sub(t) > maxAge || t.After(now.Add(5*time.Minute)) {
			return fmt.Errorf("%s verification overdue", name)
		}
	}
	return nil
}
