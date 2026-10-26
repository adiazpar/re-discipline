package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var updatesMu sync.Mutex
var updates = map[string]struct {
	at     time.Time
	result any
}{}

func updateOperator(ctx context.Context, origin string, explicit bool) (any, error) {
	root := pluginRoot()
	if root == "" {
		return map[string]string{"status": "unmanaged"}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(origin)))
	if _, err := os.Stat(filepath.Join(home, ".re-discipline", "servers", "credentials", key+".json")); os.IsNotExist(err) {
		return map[string]string{"status": "unmanaged"}, nil
	}
	updatesMu.Lock()
	defer updatesMu.Unlock()
	if cached, ok := updates[origin]; ok && !explicit && time.Since(cached.at) < time.Minute {
		if report, ok := cached.result.(map[string]any); ok && report["status"] == "failed" {
			return report, fmt.Errorf("managed update failed; use server.update to retry and check the private deployment log")
		}
		return cached.result, nil
	}
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	args := []string{filepath.Join(root, "scripts", "community-update.py"), "ensure", "--service", origin}
	if explicit {
		args = append(args, "--retry")
	}
	wait, cancel := context.WithTimeout(ctx, 190*time.Second)
	defer cancel()
	cmd := exec.CommandContext(wait, python, args...)
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("managed server update could not be checked; inspect the private deployment configuration")
	}
	var report map[string]any
	if err = json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("invalid managed update response")
	}
	updates[origin] = struct {
		at     time.Time
		result any
	}{time.Now(), report}
	if report["status"] == "failed" {
		return report, fmt.Errorf("managed update failed; use server.update to retry and check the private deployment log")
	}
	return report, nil
}
