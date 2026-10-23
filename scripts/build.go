// Canonical build: go run ./scripts/build.go [-all] [-check]
// Only developers need Go. Plugin users run the committed native artifacts.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var targets = []string{"windows-amd64", "darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"}

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build() error {
	all := flag.Bool("all", false, "build every shipped platform")
	check := flag.Bool("check", false, "rebuild to a temporary directory and compare shipped artifacts")
	target := flag.String("target", runtime.GOOS+"-"+runtime.GOARCH, "GOOS-GOARCH target")
	output := flag.String("output", "", "override output file for a single target")
	flag.Parse()
	if *all && *output != "" {
		return fmt.Errorf("-output cannot be combined with -all")
	}
	if *check && *output != "" {
		return fmt.Errorf("-output cannot be combined with -check")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	var version string
	for _, name := range []string{".claude-plugin/plugin.json", ".codex-plugin/plugin.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return fmt.Errorf("run from the repository root: %w", err)
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.Version == "" || (version != "" && version != manifest.Version) {
			return fmt.Errorf("manifest version mismatch: %s", name)
		}
		version = manifest.Version
	}
	data, err := os.ReadFile(filepath.Join(root, "retrieval/go.mod"))
	if err != nil {
		return err
	}
	toolchain := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "toolchain ") {
			toolchain = strings.TrimSpace(strings.TrimPrefix(line, "toolchain "))
		}
	}
	if toolchain == "" {
		return fmt.Errorf("missing pinned toolchain in retrieval/go.mod")
	}
	wanted := []string{*target}
	if *all {
		wanted = targets
	}
	temp, err := os.MkdirTemp("", "re-discipline-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for _, platform := range wanted {
		valid := false
		for _, supported := range targets {
			if platform == supported {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unsupported target %q (supported: %s)", platform, strings.Join(targets, ", "))
		}
		parts := strings.Split(platform, "-")
		artifact := filepath.Join(root, "bin", platform, "re-search")
		if platform == "windows-amd64" {
			artifact = filepath.Join(root, "bin/re-search.exe")
		}
		out := artifact
		if *output != "" {
			out, err = filepath.Abs(*output)
			if err != nil {
				return err
			}
		}
		if *check {
			out = filepath.Join(temp, platform, filepath.Base(artifact))
		}
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-X main.version="+version, "-o", out, "./cmd/re-search")
		cmd.Dir = filepath.Join(root, "retrieval")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN="+toolchain, "CGO_ENABLED=0", "GOOS="+parts[0], "GOARCH="+parts[1], "GOAMD64=v1", "GOARM64=v8.0", "GOFLAGS=", "GOWORK=off")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", platform, err)
		}
		if *check {
			committed, err := os.ReadFile(artifact)
			if err != nil {
				return err
			}
			rebuilt, err := os.ReadFile(out)
			if err != nil {
				return err
			}
			if !bytes.Equal(committed, rebuilt) {
				return fmt.Errorf("stale binary: %s; run go run ./scripts/build.go -all", artifact)
			}
			fmt.Printf("verified %s (version %s)\n", platform, version)
		} else {
			fmt.Printf("built %s (version %s)\n", out, version)
		}
	}
	return nil
}
