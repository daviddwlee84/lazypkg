package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestSelectionPrecedenceAndSetOrder(t *testing.T) {
	c := Config{Managers: []string{"brew"}, ManagerOrder: []string{"brew", "npm", "uv"}, ManagerSets: map[string][]string{"work": {"uv", "npm", "brew"}}, DefaultManagerSet: "work"}
	for _, test := range []struct {
		q    domain.PackageQuery
		want []string
	}{{domain.PackageQuery{}, []string{"uvx", "npm", "brew"}}, {domain.PackageQuery{Managers: []string{"gem", "go", "gem"}}, []string{"gem", "go"}}, {domain.PackageQuery{Set: "work"}, []string{"uvx", "npm", "brew"}}} {
		got, err := c.Select(test.q)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%+v: %v %v", test.q, got, err)
		}
	}
	got, err := c.Order(domain.PackageQuery{})
	if err != nil || !reflect.DeepEqual(got, []string{"uvx", "npm", "brew"}) {
		t.Fatal(got, err)
	}
	got, err = c.Order(domain.PackageQuery{Managers: []string{"uvx", "go", "brew", "gem"}})
	if err != nil || !reflect.DeepEqual(got, []string{"uvx", "go", "brew", "gem"}) {
		t.Fatal(got, err)
	}
	group, err := c.Select(domain.PackageQuery{Group: "rust"})
	if err != nil || !slices.Contains(group, "cargo") || !slices.Contains(group, "rustup") {
		t.Fatal(group, err)
	}
	for _, q := range []domain.PackageQuery{{Managers: []string{"npm"}, Group: "node"}, {Group: "node", Set: "work"}, {Set: "absent"}, {Group: "absent"}, {Managers: []string{"absent"}}, {Managers: []string{}}} {
		if _, err := c.Select(q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
}

func TestDefaultPreferenceRespectsGlobalPriority(t *testing.T) {
	c := Config{Managers: []string{"uv", "brew", "go"}, ManagerOrder: []string{"go", "brew"}}
	if got := c.Preferences().Default; !reflect.DeepEqual(got, []string{"go", "brew", "uvx"}) {
		t.Fatal(got)
	}
	if got := c.Preferences().Order; got[0] != "go" || got[1] != "brew" {
		t.Fatal(got)
	}
	explicit, err := c.Order(domain.PackageQuery{Managers: []string{"uv", "brew", "go"}})
	if err != nil || !reflect.DeepEqual(explicit, []string{"uvx", "brew", "go"}) {
		t.Fatal(explicit, err)
	}
}

func TestPreferenceGroupsMatchQueryPriorityWhileSetsKeepOrder(t *testing.T) {
	c := Config{ManagerOrder: []string{"rustup", "uv", "npm"}, ManagerSets: map[string][]string{"rust": {"cargo", "rustup"}}}
	prefs := c.Preferences()
	for _, group := range []string{"rust", "python", "node"} {
		want, err := c.Order(domain.PackageQuery{Group: group})
		if err != nil || !reflect.DeepEqual(prefs.Groups[group], want) {
			t.Fatalf("%s: preferences %v, query %v, error %v", group, prefs.Groups[group], want, err)
		}
	}
	if !reflect.DeepEqual(prefs.Groups["rust"], []string{"rustup", "cargo"}) || prefs.Groups["python"][0] != "uvx" {
		t.Fatal("group preferences ignored manager_order", prefs.Groups)
	}
	if !reflect.DeepEqual(prefs.Sets["rust"], []string{"cargo", "rustup"}) {
		t.Fatal("global priority reordered an explicit set", prefs.Sets)
	}
	prefs.Groups["rust"][0] = "changed"
	if c.Preferences().Groups["rust"][0] != "rustup" {
		t.Fatal("preferences group was not detached")
	}
}
func TestLegacyDefaultAndMouse(t *testing.T) {
	c := Config{Managers: []string{"uv", "brew"}}
	ids, err := c.Select(domain.PackageQuery{})
	if err != nil || !reflect.DeepEqual(ids, []string{"uvx", "brew"}) {
		t.Fatal(ids, err)
	}
	if !c.Preferences().Mouse {
		t.Fatal("mouse should default on")
	}
	no := false
	c.Mouse = &no
	if c.Preferences().Mouse {
		t.Fatal("explicit mouse false ignored")
	}
	defaults, err := (Config{}).Select(domain.PackageQuery{})
	if err != nil || len(defaults) != 19 || slices.Contains(defaults, "uv-pip") || !slices.Contains(defaults, "go") || !slices.Contains(defaults, "gh-ext") {
		t.Fatal(defaults, err)
	}
	prefs := c.Preferences()
	prefs.Groups["rust"][0] = "corrupt"
	if slices.Contains(c.Preferences().Groups["rust"], "corrupt") {
		t.Fatal("preferences were not detached")
	}
}
func TestLoadRejectsInvalidManagerConfiguration(t *testing.T) {
	for _, data := range []string{"managers = ['not-a-manager']", "manager_order = ['nope']", "default_manager_set = 'missing'", "[manager_sets]\n'bad name'=['brew']", "[manager_sets]\nempty=[]", "[manager_sets]\ndev=['nope']", "mouse='false'"} {
		t.Run(data, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(p, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("accepted %s", data)
			}
		})
	}
}
func TestCachePathAndTypedPreferences(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("mouse=false\nmanager_order=['uv','npm']\n[manager_sets]\nweb=['npm','uv']\n"), 0600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.CacheDir) {
		t.Fatal(c.CacheDir)
	}
	if c.Preferences().Mouse || !reflect.DeepEqual(c.Preferences().Sets["web"], []string{"npm", "uvx"}) {
		t.Fatal(c.Preferences())
	}
}
