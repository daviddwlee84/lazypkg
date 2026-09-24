package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func loadFixture(t *testing.T, text string) (Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0640); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

func TestSaveSetPreservesUnrelatedText(t *testing.T) {
	text := "# personal preferences\nmpm_path = '/my/mpm' # retain this\nmouse = false\ndefault_manager_set = 'old' # default note\n\n[manager_sets]\n# work selection\nold = [\n  'brew', # related old member\n]\nother = ['gem'] # keep other\n\n[future]\nvalue = 17 # preserve future values\n"
	c, path := loadFixture(t, text)
	updated, err := c.SaveSet("old", []string{"uv", "npm", "uvx"}, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, keep := range []string{"# personal preferences", "mpm_path = '/my/mpm' # retain this", "mouse = false", "# default note", "# work selection", "other = ['gem'] # keep other", "[future]\nvalue = 17 # preserve future values"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	if !reflect.DeepEqual(updated.ManagerSets["old"], []string{"uvx", "npm"}) {
		t.Fatal(updated.ManagerSets)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := reloaded.Select(domain.PackageQuery{})
	if err != nil || !reflect.DeepEqual(ids, []string{"uvx", "npm"}) {
		t.Fatal(ids, err)
	}
	if _, err := updated.SaveSet("new", []string{"go"}, false); err != nil {
		t.Fatal("updated config could not save again:", err)
	}
}

func TestSaveSetSupportsInlineAndDottedLayouts(t *testing.T) {
	for _, text := range []string{"# top\nmanager_sets = { old=['npm'], keep=['gem'] } # inline note\nmouse=false\n", "manager_sets.old=['npm'] # dotted note\nmanager_sets.keep=['gem']\n[future]\nfoo='bar'\n", "[manager_sets]\nold=['npm']\nkeep=['gem']\n[future]\nfoo='bar'\n"} {
		t.Run(text, func(t *testing.T) {
			c, path := loadFixture(t, text)
			c, err := c.SaveSet("old", []string{"cargo"}, false)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.SaveSet("new", []string{"go", "uv-pip"}, true)
			if err != nil {
				t.Fatal(err)
			}
			check, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(check.ManagerSets["old"], []string{"cargo"}) || !reflect.DeepEqual(check.ManagerSets["keep"], []string{"gem"}) || check.DefaultManagerSet != "new" {
				t.Fatal(check)
			}
		})
	}
}
func TestSaveSetAddsRootKeysBeforeOtherTables(t *testing.T) {
	c, path := loadFixture(t, "# untouched header\n[future]\nvalue='kept'\n")
	_, err := c.SaveSet("dev", []string{"uv", "go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultManagerSet != "dev" || !reflect.DeepEqual(got.ManagerSets["dev"], []string{"uvx", "go"}) {
		t.Fatal(got)
	}
}
func TestSaveSetRejectsConcurrentChanges(t *testing.T) {
	c, path := loadFixture(t, "mouse=true\n")
	other, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveSet("first", []string{"go"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := other.SaveSet("second", []string{"gem"}, false); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatal("stale config overwrite accepted", err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "second") {
		t.Fatal("concurrent save overwrote content")
	}
}
func TestSaveSetPreservesSymlink(t *testing.T) {
	c, target := loadFixture(t, "mouse=false\n")
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	c, err := Load(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveSet("tools", []string{"go"}, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration symlink replaced", err)
	}
	check, err := Load(target)
	if err != nil || check.DefaultManagerSet != "tools" {
		t.Fatal(check, err)
	}
}
func TestSaveSetRejectsInvalidNamesAndEmptySets(t *testing.T) {
	c, _ := loadFixture(t, "")
	for _, test := range []struct {
		name string
		ids  []string
	}{{"bad name", []string{"go"}}, {"empty", nil}, {"nope", []string{"unknown"}}} {
		if _, err := c.SaveSet(test.name, test.ids, false); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
}

func TestSaveSetCreatesMissingDefaultConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.sourceExists {
		t.Fatal("fixture unexpectedly existed")
	}
	c, err = c.SaveSet("new", []string{"go", "uv"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.SaveSet("other", []string{"gem"}, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(c.Path)
	if err != nil || loaded.DefaultManagerSet != "new" || !reflect.DeepEqual(loaded.ManagerSets["new"], []string{"go", "uvx"}) {
		t.Fatal(loaded, err)
	}
}

func TestSaveSetRetainsInlineTableTrailingComment(t *testing.T) {
	c, path := loadFixture(t, "manager_sets = {\n old = ['npm'], # retained comment\n}\n")
	_, err := c.SaveSet("new", []string{"go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# retained comment") {
		t.Fatal(string(data))
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(loaded.ManagerSets["new"], []string{"go"}) {
		t.Fatal(loaded, err)
	}
}
