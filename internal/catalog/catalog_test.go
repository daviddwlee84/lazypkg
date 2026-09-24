package catalog

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestGeneratedCatalogAndVersion(t *testing.T) {
	var data struct {
		Version  string  `json:"backend_version"`
		Managers []Entry `json:"managers"`
	}
	if err := json.Unmarshal(artifact, &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != domain.MPMVersion || len(data.Managers) != 149 {
		t.Fatalf("wrong catalog: %s, %d", data.Version, len(data.Managers))
	}
	seen := map[string]bool{}
	last := ""
	for _, e := range All() {
		if e.ID <= last || seen[e.BackendID] {
			t.Fatalf("unsorted or duplicate entry: %+v", e)
		}
		seen[e.BackendID] = true
		last = e.ID
		if !strings.Contains(e.SourceURL, "/v"+domain.MPMVersion+"/") {
			t.Fatal(e.SourceURL)
		}
		if e.Scope != "global" && e.Scope != "environment" && e.Scope != "unknown" {
			t.Fatal(e.Scope)
		}
	}
}
func TestAliasesCapabilitiesAndScope(t *testing.T) {
	for input, want := range map[string]string{"uv": "uvx", "UV": "uvx", "uvx": "uvx", "uv-pip": "uv-pip"} {
		if Normalize(input) != want {
			t.Fatal(input)
		}
	}
	if BackendID("uv-pip") != "uv" || BackendID("uv") != "uvx" {
		t.Fatal("uv adapter alias collision")
	}
	for id, want := range map[string][]string{"go": {"installed", "install"}, "cargo": {"installed", "search", "install", "remove"}} {
		e, ok := Lookup(id)
		if !ok || !reflect.DeepEqual(e.Capabilities, want) {
			t.Fatalf("%s: %+v", id, e)
		}
	}
	for id, scope := range map[string]string{"uv-pip": "environment", "pip": "environment", "gem": "global", "rustup": "global", "asdf": "unknown"} {
		e, _ := Lookup(id)
		if e.Scope != scope {
			t.Fatalf("%s: %s", id, e.Scope)
		}
	}
	npm, _ := Lookup("npm")
	if npm.Requirement != ">=11.10.0" || !strings.Contains(npm.Reason, "min-release-age") {
		t.Fatal(npm)
	}
	if len(DefaultIDs()) != 19 || !slices.Contains(DefaultIDs(), "gh-ext") || slices.Contains(DefaultIDs(), "uv-pip") {
		t.Fatal(DefaultIDs())
	}
}
func TestGroupsAndDetachedMetadata(t *testing.T) {
	groups := Groups()
	if !slices.Contains(groups["rust"], "cargo") || !slices.Contains(groups["rust"], "rustup") || !slices.Contains(groups["python"], "uv-pip") {
		t.Fatal(groups)
	}
	groups["rust"][0] = "changed"
	if slices.Contains(Groups()["rust"], "changed") {
		t.Fatal("groups mutated global catalog")
	}
	e, _ := Lookup("npm")
	e.Capabilities[0] = "changed"
	again, _ := Lookup("npm")
	if again.Capabilities[0] == "changed" {
		t.Fatal("lookup exposed shared metadata")
	}
}

func TestComponentMetadata(t *testing.T) {
	for _, tc := range []struct{ id, kind, subject, launcher string }{
		{"fisher", "shell", "component", "fish"},
		{"zim", "shell", "component", "zsh"},
		{"lazy", "hosted", "component", "nvim"},
		{"mason", "hosted", "component", "nvim"},
		{"vim-pack", "hosted", "launcher", "nvim"},
		{"emacs", "hosted", "launcher", "emacs"},
		{"yazi", "executable", "component", "ya"},
		{"gh-ext", "hosted", "launcher", "gh"},
	} {
		e, ok := Lookup(tc.id)
		if !ok || e.ComponentKind != tc.kind || e.VersionSubject != tc.subject || e.Launcher != tc.launcher {
			t.Fatalf("%s: %+v", tc.id, e)
		}
	}
}

// Set LAZYPKG_CATALOG_PYTHON to the interpreter provisioned from the checked-in
// requirements to run the generator parity check. Runtime tests need no Python.
func TestGeneratorParity(t *testing.T) {
	python := os.Getenv("LAZYPKG_CATALOG_PYTHON")
	if python == "" {
		t.Skip("set LAZYPKG_CATALOG_PYTHON to the pinned generator environment")
	}
	script := filepath.Join("..", "..", "scripts", "generate_catalog.py")
	cmd := exec.Command(python, script, "--check")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
