package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	MPMPath           string              `toml:"mpm_path" json:"mpm_path,omitempty"`
	Managers          []string            `toml:"managers" json:"managers,omitempty"`
	ManagerOrder      []string            `toml:"manager_order" json:"manager_order,omitempty"`
	ManagerSets       map[string][]string `toml:"manager_sets" json:"manager_sets,omitempty"`
	DefaultManagerSet string              `toml:"default_manager_set" json:"default_manager_set,omitempty"`
	Mouse             *bool               `toml:"mouse" json:"mouse,omitempty"`
	QueryCache        *bool               `toml:"query_cache" json:"query_cache,omitempty"`
	TimeoutSeconds    int                 `toml:"timeout_seconds" json:"timeout_seconds"`
	Path              string              `toml:"-" json:"config_path"`
	DataDir           string              `toml:"-" json:"data_dir"`
	CacheDir          string              `toml:"-" json:"cache_dir"`
	sourceDigest      [sha256.Size]byte
	sourceExists      bool
	loaded            bool
	resolvedPath      string
}

func xdg(key, home, suffix string) string {
	if p := os.Getenv(key); filepath.IsAbs(p) {
		return filepath.Join(p, "lazypkg")
	}
	return filepath.Join(home, suffix, "lazypkg")
}
func Paths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", "", fmt.Errorf("cannot resolve home directory")
	}
	if runtime.GOOS == "windows" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", "", err
		}
		local, err := os.UserCacheDir()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(cfg, "lazypkg", "config.toml"), filepath.Join(local, "lazypkg", "data"), nil
	}
	return filepath.Join(xdg("XDG_CONFIG_HOME", home, ".config"), "config.toml"), xdg("XDG_DATA_HOME", home, ".local/share"), nil
}
func Load(path string) (Config, error) {
	def, data, err := Paths()
	if err != nil {
		return Config{}, err
	}
	explicit := path != ""
	if !explicit {
		path = def
	}
	c := Config{Path: path, DataDir: data, TimeoutSeconds: 30}
	if runtime.GOOS == "windows" {
		base, e := os.UserCacheDir()
		if e != nil {
			return c, e
		}
		c.CacheDir = filepath.Join(base, "lazypkg", "cache")
	} else {
		home, e := os.UserHomeDir()
		if e != nil {
			return c, e
		}
		c.CacheDir = xdg("XDG_CACHE_HOME", home, ".cache")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			return c, err
		}
	} else if err = toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.loaded = true
	c.sourceExists = err == nil
	c.sourceDigest = sha256.Sum256(b)
	c.resolvedPath, _ = filepath.Abs(path)
	if c.sourceExists {
		resolved, e := filepath.EvalSymlinks(c.resolvedPath)
		if e != nil {
			return c, e
		}
		c.resolvedPath = resolved
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 600 {
		return c, fmt.Errorf("timeout_seconds must be between 1 and 600")
	}
	if p := os.Getenv("LAZYPKG_MPM"); p != "" {
		c.MPMPath = p
	}
	if err := c.validate(); err != nil {
		return c, err
	}
	return c, nil
}
func (c Config) Timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }

// Preferences is a detached view suitable for CLI and TUI state. Configuration
// loaded from disk is validated; Select/Order validate programmatic values too.
func (c Config) Preferences() domain.ManagerPreferences {
	defaults, _ := c.Order(domain.PackageQuery{})
	all := []string{}
	for _, entry := range catalog.All() {
		all = append(all, entry.ID)
	}
	order, _ := c.priorityOrder(all)
	sets := map[string][]string{}
	for k, v := range c.ManagerSets {
		sets[k], _ = normalizeIDs(v)
	}
	groups := catalog.Groups()
	for name, ids := range groups {
		if ordered, err := c.priorityOrder(ids); err == nil {
			groups[name] = ordered
		}
	}
	mouse := true
	if c.Mouse != nil {
		mouse = *c.Mouse
	}
	return domain.ManagerPreferences{Default: defaults, Order: order, Sets: sets, Groups: groups, DefaultSet: c.DefaultManagerSet, Mouse: mouse}
}
