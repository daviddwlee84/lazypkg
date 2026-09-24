package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigAndEnvironmentPrecedence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("mpm_path = '/configured/mpm'\ntimeout_seconds = 12\nmanagers = ['brew', 'uvx']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAZYPKG_MPM", "/override/mpm")
	c, err := Load(p)
	if err != nil || c.TimeoutSeconds != 12 || c.MPMPath != "/override/mpm" || len(c.Managers) != 2 {
		t.Fatal(c, err)
	}
}
func TestExplicitMissingAndMalformedConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if _, err := Load(p); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	os.WriteFile(p, []byte("timeout_seconds = 0"), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("invalid timeout accepted")
	}
	os.WriteFile(p, []byte("this = ["), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("malformed config accepted")
	}
}
func TestXDGRelativeIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	cfg, data, err := Paths()
	if err != nil || !filepath.IsAbs(cfg) || !filepath.IsAbs(data) {
		t.Fatal(cfg, data, err)
	}
}
