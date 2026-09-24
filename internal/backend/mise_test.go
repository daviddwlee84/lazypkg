package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func TestMiseGlobalQueryIsolatesProjectAndEnvironment(t *testing.T) {
	t.Setenv("MISE_NODE_VERSION", "22.0.0")
	var isolated string
	m := Mise{Path: "mise", Dir: "project", Env: map[string]string{"MISE_NODE_VERSION": "22.0.0", "MISE_ENV": "production"}, Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if c.Dir == "project" || c.Dir == "" || c.Env["MISE_CEILING_PATHS"] != c.Dir {
			t.Fatal("global query inherited project", c)
		}
		if c.Env["MISE_NODE_VERSION"] != "" || c.Env["MISE_ENV"] != "production" {
			t.Fatal("wrong environment isolation", c)
		}
		found := false
		for _, k := range c.Unset {
			if k == "MISE_NODE_VERSION" {
				found = true
			}
		}
		if !found {
			t.Fatal("shell version override was retained")
		}
		isolated = c.Dir
		return process.Result{Stdout: `{"node":[{"version":"20.0.0"}]}`}, nil
	}}}
	if _, err := m.globalOutput(context.Background(), "ls", "--global", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(isolated); !os.IsNotExist(err) {
		t.Fatal("global query directory leaked")
	}
}

func TestMiseUpdateRecognizesAlreadyInstalledLatest(t *testing.T) {
	m := Mise{Path: "mise", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		switch strings.Join(c.Args, " ") {
		case "ls --installed --json":
			return process.Result{Stdout: `{"node":[{"version":"20.0.0"},{"version":"22.0.0"}]}`}, nil
		case "ls --global --json", "ls --current --json":
			return process.Result{Stdout: `{"node":[{"version":"20.0.0"}]}`}, nil
		case "outdated --json --bump":
			return process.Result{Stdout: `{"node":{"current":"20.0.0","latest":"22.0.0"}}`}, nil
		}
		return process.Result{}, fmt.Errorf("unexpected command %v", c.Args)
	}}}
	s, err := m.Outdated(context.Background())
	if err != nil || len(s.Packages) != 1 || !s.Packages[0].LatestInstalled {
		t.Fatal(s, err)
	}
}

// Opt-in native contract check. It creates private config/install metadata only;
// no runtime is downloaded and no user/project configuration is changed.
func TestMiseLiveGlobalScope(t *testing.T) {
	path := os.Getenv("LAZYPKG_TEST_MISE")
	if path == "" {
		t.Skip("set LAZYPKG_TEST_MISE to opt into the native mise contract check")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	data := filepath.Join(root, "data")
	global := filepath.Join(root, "global.toml")
	for _, dir := range []string{project, filepath.Join(data, "installs", "node", "20.0.0", "bin"), filepath.Join(data, "installs", "node", "22.0.0", "bin")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(global, []byte("[tools]\nnode = '20.0.0'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	projectContent := []byte("[tools]\nnode = '22.0.0'\n")
	if err := os.WriteFile(filepath.Join(project, "mise.toml"), projectContent, 0600); err != nil {
		t.Fatal(err)
	}
	m := Mise{Path: path, Dir: project, Runner: process.ExecRunner{}, Timeout: 10 * time.Second, Env: map[string]string{"MISE_GLOBAL_CONFIG_FILE": global, "MISE_DATA_DIR": data, "MISE_STATE_DIR": filepath.Join(root, "state"), "MISE_CACHE_DIR": filepath.Join(root, "cache"), "MISE_CONFIG_DIR": filepath.Join(root, "config"), "MISE_TRUSTED_CONFIG_PATHS": root, "MISE_NODE_VERSION": "22.0.0"}}
	s, err := m.Installed(context.Background())
	if err != nil || len(s.Issues) > 0 {
		t.Fatal(s, err)
	}
	globalFound, currentFound := false, false
	for _, p := range s.Packages {
		if p.ID == "node" && p.Version == "20.0.0" {
			globalFound = p.Global && !p.Active
		}
		if p.ID == "node" && p.Version == "22.0.0" {
			currentFound = p.Active && !p.Global
		}
	}
	if !globalFound || !currentFound {
		t.Fatalf("project override hid global selection: %#v", s)
	}
	content, err := os.ReadFile(filepath.Join(project, "mise.toml"))
	if err != nil || string(content) != string(projectContent) {
		t.Fatal("project config changed", err)
	}
}
