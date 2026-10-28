// Package safepath answers one question about the filesystem -- does
// this path lie inside that directory? -- the one way grain asks it.
//
// The obvious answer, strings.HasPrefix(filepath.Clean(p), root), is
// lexical: it sees the names in the path and not what they point at, so
// a symlink inside the root that points out of it passes (#2410, #2393).
// It also gets the boundary wrong without a trailing separator, since
// "/srv/work-other" begins with "/srv/work". Within resolves symlinks on
// both sides and compares the results with filepath.Rel instead.
// tests/security's path-containment ratchet holds the tree to using it.
package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutside is the error Within wraps when a path resolves outside its
// root.
var ErrOutside = errors.New("path is outside its root")

// Within resolves p -- relative to root unless it is absolute -- through
// every symlink that exists, and returns the resolved path when it lies
// inside root (or is root itself), which must exist. A path that
// resolves anywhere else, through .. or through a link, is refused with
// an error wrapping ErrOutside.
//
// Components that do not exist yet are allowed, so a file about to be
// created can be checked; a dangling symlink is not, since what it would
// create is wherever the link points. Callers must use the path Within
// returns rather than the one they passed in: it is the one that was
// checked. What it cannot do is stop something swapping a component for
// a symlink between the check and the open -- where the tree under root
// is written by somebody else concurrently, open through os.Root too.
func Within(root, p string) (string, error) {
	if root == "" {
		return "", errors.New("safepath: empty root")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("safepath: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("safepath: resolving root: %w", err)
	}
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(absRoot, full)
	}
	resolved, err := resolve(filepath.Clean(full))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("safepath: %s: %w", p, ErrOutside)
	}
	return resolved, nil
}

// resolve evaluates every symlink in the longest prefix of full that
// exists and appends the components after it, which do not.
func resolve(full string) (string, error) {
	var missing []string
	cur := full
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{real}, missing...)...), nil
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			// It is there but does not resolve: a dangling link, a loop,
			// or a component that cannot be read.
			return "", fmt.Errorf("safepath: resolving %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("safepath: resolving %s: %w", full, err)
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
}
