package process

import (
	"context"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvironmentIsolation(t *testing.T) {
	env := Environment([]string{"MPM_CONFIG=bad", "MPM_DRY_RUN=1", "PATH=old", "MISE_ENV=test"}, domain.Command{Unset: []string{"MPM_*"}, Env: map[string]string{"PATH": "new"}})
	s := strings.Join(env, "\n")
	if strings.Contains(s, "MPM_") || !strings.Contains(s, "PATH=new") || !strings.Contains(s, "MISE_ENV=test") {
		t.Fatal(s)
	}
}
func TestChildPATHCanResolveNewManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX script; Windows lookup covered by platform CI and diagnostics tests")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "new-manager")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := (ExecRunner{}).Output(context.Background(), domain.Command{Path: "new-manager", Args: []string{"literal; $(not-executed)"}, Env: map[string]string{"PATH": dir}})
	if err != nil || r.Stdout != "literal; $(not-executed)" {
		t.Fatal(r, err)
	}
}

func TestChildPATHDoesNotFallBackToParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX parent PATH fixture")
	}
	_, err := (ExecRunner{}).Output(context.Background(), domain.Command{Path: "sh", Args: []string{"-c", "exit 0"}, Env: map[string]string{"PATH": t.TempDir()}})
	if err == nil {
		t.Fatal("fell back to parent PATH")
	}
}

func TestWindowsEnvironmentCaseFolding(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment semantics")
	}
	env := Environment([]string{"Path=old", "mpm_config=bad"}, domain.Command{Unset: []string{"MPM_*"}, Env: map[string]string{"PATH": "new"}})
	if len(env) != 1 || env[0] != "PATH=new" {
		t.Fatal(env)
	}
}
