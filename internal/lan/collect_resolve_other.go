//go:build !windows

package lan

import "path/filepath"

func resolveCollectPath(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
