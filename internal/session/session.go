// Package session prepares the sandbox a wrapped command runs in: a
// directory of shim symlinks prepended to PATH, the environment protocol
// the shims read, and PATH resolution helpers that can exclude the shim
// directory (so a record-mode shim finds the real tool, not itself).
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ValidateName rejects command names that cannot safely become a shim
// symlink: path separators would escape the shim directory, and "pathshim"
// itself would shadow the CLI inside the session.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("empty command name")
	case name == "." || name == "..":
		return fmt.Errorf("invalid command name %q", name)
	case strings.ContainsRune(name, '/'):
		return fmt.Errorf("command name %q must be bare (no path separators)", name)
	case name == "pathshim":
		return fmt.Errorf("cannot shim %q (it is the tool itself)", name)
	}
	return nil
}

// CreateShims populates dir with one symlink per command name, each
// pointing at the pathshim executable. The shim dispatches on its own
// argv[0], so a single binary serves every shimmed command.
func CreateShims(dir, exe string, names []string) error {
	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return err
		}
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("creating shim for %q: %w", name, err)
		}
	}
	return nil
}

// Executable returns the absolute path of the running pathshim binary,
// which is what every shim symlink must point at.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the pathshim binary: %w", err)
	}
	return exe, nil
}

// BuildEnv derives the child environment from base: PATH gets shimDir
// prepended, any stale PATHSHIM_* variables are dropped, and vars are
// appended in sorted key order for deterministic construction.
func BuildEnv(base []string, shimDir string, vars map[string]string) []string {
	out := make([]string, 0, len(base)+len(vars))
	sawPath := false
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "PATHSHIM_") {
			continue // never inherit a stale session
		}
		if key == "PATH" {
			out = append(out, "PATH="+PrependPath(kv[len("PATH="):], shimDir))
			sawPath = true
			continue
		}
		out = append(out, kv)
	}
	if !sawPath {
		out = append(out, "PATH="+shimDir)
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// PrependPath puts dir at the front of a PATH-style list, removing any
// existing occurrence so repeated sessions never stack.
func PrependPath(pathVar, dir string) string {
	parts := []string{dir}
	for _, p := range filepath.SplitList(pathVar) {
		if p == dir {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

// LookPath resolves name against the entries of pathVar. Names containing
// a path separator are returned as-is (after an executability check), the
// same contract the shell uses. This exists because os/exec.LookPath
// consults the process's own PATH, while the wrapped command must be
// resolved against the session PATH that includes the shim directory.
func LookPath(name, pathVar string) (string, error) {
	return lookPath(name, pathVar, "")
}

// LookPathExcluding resolves name like LookPath but skips excludeDir, so a
// record-mode shim invoked as "git" finds the real git rather than itself.
func LookPathExcluding(name, pathVar, excludeDir string) (string, error) {
	return lookPath(name, pathVar, excludeDir)
}

func lookPath(name, pathVar, excludeDir string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("%s: not an executable file", name)
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			dir = "." // historical PATH semantics: empty entry means cwd
		}
		if excludeDir != "" && sameDir(dir, excludeDir) {
			continue
		}
		cand := filepath.Join(dir, name)
		if isExecutable(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s: executable not found in PATH", name)
}

// sameDir compares directories both lexically and by identity (device and
// inode), so /tmp/x and a symlinked alias of it are treated as the same
// shim directory.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
