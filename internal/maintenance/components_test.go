package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestHostedComponentsNeverUpgradeTheirLauncher(t *testing.T) {
	for _, id := range []string{"fisher", "antigen", "antidote", "oh-my-fish", "zim", "zinit", "zplug", "lazy", "mason", "vim-pack", "emacs", "micro"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			launcher := executableName(id)
			path := filepath.Join(dir, "Cellar", launcher, "1.0.0", "bin", launcher)
			file(t, path, "host fixture")
			file(t, filepath.Join(dir, "bin", "brew"), "owner fixture")
			r := &fakeRunner{output: func(c domain.Command) (string, error) {
				t.Errorf("hosted component caused owner/host probe: %v", c)
				return "", nil
			}}
			e := New(r, filepath.Join(dir, "cache"))
			e.Env = map[string]string{"PATH": filepath.Join(dir, "bin")}
			m := domain.Manager{ID: id, Path: path, ReasonCode: "component_missing", Status: "component missing"}
			rows, err := e.Check(context.Background(), []domain.Manager{m}, false)
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			h := rows[0]
			if h.ApplySupported || h.Strategy != "" || h.OwnerPath != "" || h.ComponentKind == "executable" || h.Launcher != launcher {
				t.Fatalf("component routed to host: %+v", h)
			}
			if !strings.Contains(h.Recommendation, "component") && !strings.Contains(h.Recommendation, "plugin") {
				t.Fatal(h.Recommendation)
			}
			// A forged caller plan cannot turn a catalogued plugin into its host.
			h.ApplySupported, h.Strategy, h.ComponentKind = true, "brew-package", "executable"
			h.OwnerPath, h.OwnerPackage, h.CandidateVersion = "/brew", launcher, "2.0.0"
			p := e.plan(h)
			if p.ManagerUpdate.ApplySupported || p.Steps[0].Command.Path != "" {
				t.Fatal("hosted update was planned", p)
			}
			p.ManagerUpdate.ApplySupported = true
			if _, err := e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err == nil || len(r.runs) != 0 {
				t.Fatal("forged hosted update accepted", err, r.runs)
			}
		})
	}
}

func TestHealthCacheRejectsPreviousOwnershipSchema(t *testing.T) {
	e := New(&fakeRunner{}, t.TempDir())
	h := domain.ManagerHealth{Manager: "fisher", Fingerprint: "same-key", ApplySupported: true, Strategy: "brew-package"}
	b, _ := json.Marshal(cacheRecord{Schema: 1, Health: h})
	file(t, filepath.Join(e.CacheDir, h.Fingerprint+".json"), string(b))
	if _, ok := e.cached(h.Fingerprint); ok {
		t.Fatal("unsafe previous-schema health cache reused")
	}
	e.save(h)
	if _, ok := e.cached(h.Fingerprint); !ok {
		t.Fatal("current schema cache unreadable")
	}
}

func TestMaintenanceReadOnlyProbeGuards(t *testing.T) {
	r := &fakeRunner{output: func(c domain.Command) (string, error) {
		if c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" || c.Env["GOTOOLCHAIN"] != "local" || c.Env["GEM_HOME"] != "/chosen/gems" {
			t.Fatal(c.Env)
		}
		if len(c.Args) != 1 || c.Args[0] != "version" {
			t.Fatal("Go maintenance used a non-native version probe", c.Args)
		}
		return "1.0.0", nil
	}}
	e := New(r, "")
	e.Env = map[string]string{"MISE_AUTO_INSTALL": "1", "MISE_NOT_FOUND_AUTO_INSTALL": "true", "GOTOOLCHAIN": "auto", "GEM_HOME": "/chosen/gems"}
	if _, err := e.managerVersion(context.Background(), domain.ManagerHealth{Manager: "go", Path: "go"}); err != nil {
		t.Fatal(err)
	}
	if e.command("go", "install").Env["GOTOOLCHAIN"] != "auto" || e.Env["MISE_AUTO_INSTALL"] != "1" {
		t.Fatal("read-only guards mutated caller/mutation environment")
	}
}

func TestHealthDoesNotReclassifyFailedProbeAsCompatible(t *testing.T) {
	e := New(&fakeRunner{output: func(domain.Command) (string, error) { return "", nil }}, "")
	e.Env = map[string]string{"PATH": t.TempDir()}
	h := e.observe(context.Background(), domain.Manager{ID: "composer", Path: "/tools/composer", Version: "100.0.0", Requirement: ">=2.0.0", ReasonCode: "probe_failed"}).Health
	if h.Compatible || h.ReasonCode != "probe_failed" {
		t.Fatal(h)
	}
}

func TestHomebrewOwnershipDoesNotConflateBundledVersions(t *testing.T) {
	for _, tc := range []struct{ manager, formula, current string }{{"pip", "python@3.14", "25.0.0"}, {"gem", "ruby", "3.6.0"}, {"nimble", "nim", "0.20.0"}, {"mise", "mise", "2026.8.0"}} {
		t.Run(tc.manager, func(t *testing.T) {
			dir := canonical(t.TempDir())
			brew := filepath.Join(dir, "bin", "brew")
			file(t, brew, "brew")
			path := filepath.Join(dir, "Cellar", tc.formula, "1.0.0", "bin", executableName(tc.manager))
			file(t, path, "component")
			r := &fakeRunner{output: func(c domain.Command) (string, error) {
				if len(c.Args) == 1 && c.Args[0] == "--cellar" {
					return filepath.Join(dir, "Cellar"), nil
				}
				return fmt.Sprintf(`{"formulae":[{"name":%q,"versions":{"stable":"100.0.0"},"installed":[{"version":"1.0.0"}]}]}`, tc.formula), nil
			}}
			e := New(r, "")
			e.Env = map[string]string{"PATH": filepath.Dir(brew), "HOMEBREW_NO_AUTOREMOVE": "0", "HOMEBREW_NO_INSTALL_CLEANUP": "0"}
			p, err := e.Plan(context.Background(), domain.Manager{ID: tc.manager, Path: path, Version: tc.current, Available: true})
			if err != nil || p.ManagerUpdate.ApplySupported || p.ManagerUpdate.Owner != "brew" || p.ManagerUpdate.Strategy != "" || !strings.Contains(p.Preview, "bundled component") {
				t.Fatal(p, err)
			}
			for _, c := range r.outputs {
				if c.Env["HOMEBREW_NO_AUTOREMOVE"] != "1" || c.Env["HOMEBREW_NO_INSTALL_CLEANUP"] != "1" {
					t.Fatal(c.Env)
				}
			}
		})
	}
}
