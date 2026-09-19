package community

import (
	"fmt"
	"os"
	"path/filepath"
)

// Explicit roots must name a real directory; never silently fall back to the
// plugin directory or a different community when the workspace is unavailable.
func ProjectRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("project root must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("project root is not a directory")
	}
	return filepath.Clean(resolved), nil
}
