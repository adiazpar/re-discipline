// Canonical build: go run ./scripts/build.go [-all] [-check]
// Only developers need Go. Plugin users run the committed native artifacts.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
	server := flag.Bool("server", false, "build the Linux community service instead of the client")
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
	buildID, err := sourceDigest(root, []string{"retrieval", "server", "scripts"}, false)
	if err != nil {
		return err
	}
	schema, err := sourceDigest(root, []string{"server/database"}, true)
	if err != nil {
		return err
	}
	protocolSource, err := os.ReadFile(filepath.Join(root, "retrieval/buildinfo/buildinfo.go"))
	if err != nil {
		return err
	}
	match := regexp.MustCompile(`const Protocol = ([0-9]+)`).FindSubmatch(protocolSource)
	if len(match) != 2 {
		return fmt.Errorf("missing build protocol")
	}
	var protocol int
	if _, err := fmt.Sscanf(string(match[1]), "%d", &protocol); err != nil {
		return err
	}
	metadata, _ := json.MarshalIndent(map[string]any{"version": version, "build": buildID, "schema": schema, "protocol": protocol}, "", "  ")
	metadata = append(metadata, '\n')
	metadataPath := filepath.Join(root, "bin", "build.json")
	if *check {
		old, e := os.ReadFile(metadataPath)
		if e != nil || !bytes.Equal(old, metadata) {
			return fmt.Errorf("stale bin/build.json; run go run ./scripts/build.go -all")
		}
	} else if *output == "" {
		if err := os.WriteFile(metadataPath, metadata, 0644); err != nil {
			return err
		}
	}
	wanted := []string{*target}
	if *all {
		wanted = targets
		if *server {
			wanted = nil
		}
		wanted = append(append([]string{}, wanted...), "server-linux-amd64", "server-linux-arm64")
	} else if *server {
		wanted = []string{"server-" + *target}
	}
	temp, err := os.MkdirTemp("", "re-discipline-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for _, platform := range wanted {
		isServer := strings.HasPrefix(platform, "server-")
		platform = strings.TrimPrefix(platform, "server-")
		valid := false
		for _, supported := range targets {
			if platform == supported {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unsupported target %q (supported: %s)", platform, strings.Join(targets, ", "))
		}
		if isServer && !strings.HasPrefix(platform, "linux-") {
			return fmt.Errorf("server artifacts target Linux; use -server -target linux-amd64 or -all")
		}
		parts := strings.Split(platform, "-")
		artifact := filepath.Join(root, "bin", platform, "re-search")
		if platform == "windows-amd64" {
			artifact = filepath.Join(root, "bin/re-search.exe")
		}
		module, command := "retrieval", "./cmd/re-search"
		if isServer {
			artifact = filepath.Join(root, "bin", "server", platform, "community")
			module, command = "server", "./cmd/community"
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
		stamp := "github.com/adiazpar/re-discipline/retrieval/buildinfo."
		flags := "-X main.version=" + version + " -X " + stamp + "Version=" + version + " -X " + stamp + "ID=" + buildID + " -X " + stamp + "Schema=" + schema
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", out, command)
		cmd.Dir = filepath.Join(root, module)
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

func sourceDigest(root string, dirs []string, schemaOnly bool) (string, error) {
	var paths []string
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "-smoke.py") || strings.HasSuffix(name, "-load.py") || name == "smoke.py" {
				return nil
			}
			ext := filepath.Ext(name)
			if ext == ".go" || ext == ".sql" || (!schemaOnly && (ext == ".py" || name == "go.mod" || name == "go.sum" || name == "Dockerfile")) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		rel, _ := filepath.Rel(root, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		h.Write(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
