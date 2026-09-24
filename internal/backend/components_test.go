package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func TestShellSourceAbsenceIsNotAnOldShell(t *testing.T) {
	home := t.TempDir()
	m := &MPM{Env: map[string]string{"HOME": home, "ZIM_HOME": filepath.Join(home, "zim")}}
	e, _ := catalog.Lookup("zim")
	v := managerJSON{Path: "/opt/homebrew/bin/zsh", Executable: true}
	status, code, reason := m.componentReason(e, v)
	if status != "component missing" || code != "component_missing" || !strings.Contains(reason, "only its launcher") {
		t.Fatal(status, code, reason)
	}
	if err := os.MkdirAll(m.Env["ZIM_HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	// Presence is not successful loading, and checking it never sources the file.
	if err := os.WriteFile(filepath.Join(m.Env["ZIM_HOME"], "init.zsh"), []byte("do-not-execute"), 0600); err != nil {
		t.Fatal(err)
	}
	status, code, _ = m.componentReason(e, v)
	if status != "component unavailable" || code != "component_unavailable" {
		t.Fatal(status, code)
	}
	e, _ = catalog.Lookup("fisher")
	if _, code, _ = m.componentReason(e, v); code != "component_unavailable" {
		t.Fatal("dynamic Fish autoload absence was overstated", code)
	}
}

func TestParserMismatchAndRequirementAreSeparate(t *testing.T) {
	m := &MPM{}
	e, _ := catalog.Lookup("yazi")
	if _, code, _ := m.componentReason(e, managerJSON{Path: "/bin/ya", Executable: true}); code != "parser_mismatch" {
		t.Fatal(code)
	}
	if _, code, _ := m.componentReason(e, managerJSON{Path: "/bin/ya", Executable: true, Errors: []json.RawMessage{json.RawMessage(`"failed"`)}}); code != "probe_failed" {
		t.Fatal(code)
	}
	e, _ = catalog.Lookup("npm")
	if _, code, reason := m.componentReason(e, managerJSON{Path: "/bin/npm", Executable: true, Version: "11.6.2"}); code != "version_unsupported" || !strings.Contains(reason, "11.10.0") {
		t.Fatal(code, reason)
	}
}

func TestYaziIsolatedVersionParserSupportsLegacyAndCurrent(t *testing.T) {
	var config struct {
		MPM struct {
			Overrides map[string]struct {
				Patterns []string `json:"version_regexes"`
			} `json:"overrides"`
		} `json:"mpm"`
	}
	if err := json.Unmarshal([]byte(isolatedConfig), &config); err != nil {
		t.Fatal(err)
	}
	patterns := config.MPM.Overrides["yazi"].Patterns
	for _, tc := range []struct{ text, want string }{
		{"Ya 26.5.6 (Homebrew 2026-05-06)", "26.5.6"},
		{"Ya\n    Version: 26.8.15 (Homebrew 2026-08-15)\n    Rustc: 1.97.1", "26.8.15"},
		{"Ya\n    Rustc: 1.97.1", ""},
	} {
		got := ""
		for _, pattern := range patterns {
			r := regexp.MustCompile(pattern)
			if match := r.FindStringSubmatch(tc.text); match != nil {
				got = match[r.SubexpIndex("version")]
				break
			}
		}
		if got != tc.want {
			t.Fatalf("%q: %q != %q", tc.text, got, tc.want)
		}
	}
}

func TestMPMHomebrewMutationsCannotAutoRemoveOrClean(t *testing.T) {
	m := &MPM{Env: map[string]string{"HOMEBREW_NO_AUTOREMOVE": "0", "HOMEBREW_NO_INSTALL_CLEANUP": "0"}}
	c, cleanup, err := m.Mutation(domain.ActionRequest{Manager: "brew", Operation: "remove", Package: "jq"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if c.Env["HOMEBREW_NO_AUTOREMOVE"] != "1" || c.Env["HOMEBREW_NO_INSTALL_CLEANUP"] != "1" {
		t.Fatal(c.Env)
	}
}

func TestHostedMetadataSurvivesDiscovery(t *testing.T) {
	m := MPM{Path: "mpm", Runner: fakeRunner{output: func(c domain.Command) (process.Result, error) {
		if len(c.Args) == 1 {
			return process.Result{Stdout: "mpm 8.0.1"}, nil
		}
		return process.Result{Stdout: `{"fisher":{"supported":true,"cli_path":"/bin/fish","executable":true},"vim-pack":{"supported":true,"cli_path":"/bin/nvim","executable":true,"version":"0.11.0"}}`}, nil
	}}}
	rows, err := m.Managers(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if row.ID == "fisher" && (row.ComponentKind != "shell" || row.Launcher != "fish" || row.VersionSubject != "component" || row.ReasonCode != "component_unavailable") {
			t.Fatal(row)
		}
		if row.ID == "vim-pack" && (row.ComponentKind != "hosted" || row.Launcher != "nvim" || row.VersionSubject != "launcher") {
			t.Fatal(row)
		}
	}
}

// Opt-in read-only parser contract check against an existing pinned backend and
// Yazi installation. The only written file is the disposable mpm configuration.
func TestLiveYaziVersionParser(t *testing.T) {
	path := os.Getenv("LAZYPKG_TEST_MPM")
	if path == "" {
		t.Skip("set LAZYPKG_TEST_MPM to an existing mpm 8.0.1 for the read-only Yazi probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m := &MPM{Path: path, Runner: process.ExecRunner{}}
	r, err := m.query(ctx, "--timeout", "5", "--table-format", "json", "--yazi", "managers", "--view", "all")
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string]managerJSON
	if err := json.Unmarshal([]byte(r.Stdout), &rows); err != nil {
		t.Fatal(err)
	}
	yazi := rows["yazi"]
	if yazi.Path == "" {
		t.Skip("Yazi is not installed")
	}
	if !yazi.Available || yazi.Version == "" {
		t.Fatalf("Yazi still has no compatible version: %+v", yazi)
	}
	t.Logf("Yazi component %s via %s", yazi.Version, yazi.Path)
}
