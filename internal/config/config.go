package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	MPMPath        string   `toml:"mpm_path" json:"mpm_path,omitempty"`
	Managers       []string `toml:"managers" json:"managers,omitempty"`
	TimeoutSeconds int      `toml:"timeout_seconds" json:"timeout_seconds"`
	Path           string   `toml:"-" json:"config_path"`
	DataDir        string   `toml:"-" json:"data_dir"`
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
	b, err := os.ReadFile(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			return c, err
		}
	} else if err = toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 600 {
		return c, fmt.Errorf("timeout_seconds must be between 1 and 600")
	}
	if p := os.Getenv("LAZYPKG_MPM"); p != "" {
		c.MPMPath = p
	}
	return c, nil
}
func (c Config) Timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }
