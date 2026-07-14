// Tests for session setup: shim naming rules, PATH construction, and the
// PATH resolution that lets a record shim find the real tool while being
// the first "git" on PATH itself.
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNameAcceptsOrdinaryToolsAndRejectsUnsafeOnes(t *testing.T) {
	for _, name := range []string{"git", "docker", "kubectl", "aws-vault", "g++"} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("%q should be a valid shim name: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "usr/bin/git", "/bin/sh", "pathshim"} {
		if err := ValidateName(name); err == nil {
			t.Fatalf("%q must be rejected as a shim name", name)
		}
	}
}

func TestCreateShimsMakesOneSymlinkPerCommand(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-binary")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shimDir := filepath.Join(dir, "bin")
	os.Mkdir(shimDir, 0o755)
	if err := CreateShims(shimDir, exe, []string{"git", "docker"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "docker"} {
		target, err := os.Readlink(filepath.Join(shimDir, name))
		if err != nil || target != exe {
			t.Fatalf("shim %q should link to %q: %q %v", name, exe, target, err)
		}
	}
	if err := CreateShims(t.TempDir(), exe, []string{"a/b"}); err == nil {
		t.Fatal("bad name must be rejected")
	}
}

func TestBuildEnvPrependsShimDirAndDeduplicatesPath(t *testing.T) {
	env := BuildEnv([]string{"PATH=/usr/bin:/shim:/bin", "HOME=/home/dev"}, "/shim", nil)
	found := false
	for _, kv := range env {
		if kv == "PATH=/shim:/usr/bin:/bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("PATH not prepended/deduplicated: %v", env)
	}
}

func TestBuildEnvDropsStaleSessionVariables(t *testing.T) {
	// A replay launched from inside a record session must not inherit the
	// outer session's mode or journal.
	base := []string{"PATH=/usr/bin", "PATHSHIM_MODE=record", "PATHSHIM_LOG=/old"}
	env := BuildEnv(base, "/shim", map[string]string{EnvMode: ModeReplay})
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATHSHIM_") && kv != "PATHSHIM_MODE=replay" {
			t.Fatalf("stale session variable leaked: %q", kv)
		}
	}
}

func TestBuildEnvSortsVarsAndSynthesizesMissingPath(t *testing.T) {
	env := BuildEnv(nil, "/shim", map[string]string{"PATHSHIM_B": "2", "PATHSHIM_A": "1"})
	if len(env) != 3 || env[0] != "PATH=/shim" || env[1] != "PATHSHIM_A=1" || env[2] != "PATHSHIM_B=2" {
		t.Fatalf("want synthesized PATH + sorted vars, got %v", env)
	}
}

func writeTool(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookPathFindsFirstMatchOnly(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeTool(t, dirA, "git")
	writeTool(t, dirB, "git")
	got, err := LookPath("git", dirA+":"+dirB)
	if err != nil || got != filepath.Join(dirA, "git") {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := LookPath("terraform", dirA+":"+dirB); err == nil {
		t.Fatal("absent tool must not resolve")
	}
	// A file without the executable bit is not a candidate.
	dirC := t.TempDir()
	os.WriteFile(filepath.Join(dirC, "git"), []byte("data"), 0o644)
	if _, err := LookPath("git", dirC); err == nil {
		t.Fatal("non-executable file must not resolve")
	}
}

func TestLookPathHonorsExplicitPaths(t *testing.T) {
	dir := t.TempDir()
	tool := writeTool(t, dir, "mytool")
	got, err := LookPath(tool, "/nonexistent")
	if err != nil || got != tool {
		t.Fatalf("explicit path should bypass PATH: %q %v", got, err)
	}
	if _, err := LookPath(filepath.Join(dir, "absent"), ""); err == nil {
		t.Fatal("explicit path to a missing file must fail")
	}
}

func TestLookPathExcludingSkipsTheShimDirectory(t *testing.T) {
	shimDir, realDir := t.TempDir(), t.TempDir()
	writeTool(t, shimDir, "git")
	real := writeTool(t, realDir, "git")
	got, err := LookPathExcluding("git", shimDir+":"+realDir, shimDir)
	if err != nil || got != real {
		t.Fatalf("should skip shim dir: %q %v", got, err)
	}
	// With nothing beyond the shim dir, resolution must fail — that is how
	// "the real tool is not installed" is detected during recording.
	if _, err := LookPathExcluding("git", shimDir, shimDir); err == nil {
		t.Fatal("must fail when the shim is the only candidate")
	}
}

func TestLookPathExcludingRecognizesSymlinkedAliasOfShimDir(t *testing.T) {
	// On systems where the temp dir has a symlinked alias (macOS /tmp),
	// PATH may carry the alias while the exclude uses the resolved path.
	shimDir, realDir := t.TempDir(), t.TempDir()
	writeTool(t, shimDir, "git")
	real := writeTool(t, realDir, "git")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(shimDir, alias); err != nil {
		t.Fatal(err)
	}
	got, err := LookPathExcluding("git", alias+":"+realDir, shimDir)
	if err != nil || got != real {
		t.Fatalf("aliased shim dir should be excluded too: %q %v", got, err)
	}
}
