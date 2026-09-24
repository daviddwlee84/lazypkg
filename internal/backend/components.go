package backend

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// These locations mirror the guarded source lookups in the pinned mpm shell
// adapters. Absence means "not found by this adapter", never absent everywhere.
func shellSources(id string, env map[string]string, home string) []string {
	get := func(k, def string) string {
		if v := env[k]; v != "" {
			return v
		}
		return def
	}
	switch id {
	case "antigen":
		paths := []string{filepath.Join(home, "antigen.zsh"), filepath.Join(home, ".antigen", "antigen.zsh")}
		if env["ADOTDIR"] != "" {
			paths = append([]string{filepath.Join(env["ADOTDIR"], "antigen.zsh")}, paths...)
		}
		return paths
	case "antidote":
		paths := []string{filepath.Join(get("ZDOTDIR", home), ".antidote", "antidote.zsh")}
		for _, prefix := range []string{env["HOMEBREW_PREFIX"], "/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"} {
			if prefix != "" {
				paths = append(paths, filepath.Join(prefix, "opt", "antidote", "share", "antidote", "antidote.zsh"))
			}
		}
		return paths
	case "oh-my-fish":
		return []string{filepath.Join(get("OMF_PATH", filepath.Join(home, ".local", "share", "omf")), "init.fish")}
	case "zim":
		return []string{filepath.Join(get("ZIM_HOME", filepath.Join(get("ZDOTDIR", home), ".zim")), "init.zsh")}
	case "zinit":
		root := get("ZINIT_HOME", filepath.Join(get("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "zinit"))
		return []string{filepath.Join(root, "zinit.git", "zinit.zsh"), filepath.Join(home, ".zinit", "bin", "zinit.zsh")}
	case "zplug":
		return []string{filepath.Join(get("ZPLUG_HOME", filepath.Join(home, ".zplug")), "init.zsh")}
	}
	return nil
}

func (m *MPM) componentReason(e catalog.Entry, v managerJSON) (string, string, string) {
	if v.Path == "" {
		return "missing", "launcher_missing", "The required launcher was not found in the current environment"
	}
	if !v.Executable {
		return "not executable", "probe_failed", "The detected launcher could not be executed"
	}
	if v.Version == "" {
		if e.ComponentKind == "shell" {
			env := map[string]string{}
			for _, entry := range process.Environment(nil, domain.Command{Env: m.Env}) {
				k, value, _ := strings.Cut(entry, "=")
				env[k] = value
			}
			home := env["HOME"]
			if home == "" {
				home, _ = os.UserHomeDir()
			}
			paths := shellSources(e.ID, env, home)
			missing := len(paths) > 0
			unreadable := false
			for _, path := range paths {
				info, err := os.Stat(path)
				if err == nil && !info.IsDir() {
					missing = false
					f, err := os.Open(path)
					if err != nil {
						unreadable = true
					} else {
						f.Close()
					}
					break // mpm sources the first existing candidate only.
				} else if err != nil && !os.IsNotExist(err) {
					missing = false
					unreadable = true
				}
			}
			if missing {
				return "component missing", "component_missing", e.Name + " source was not found at the locations used by mpm 8.0.1; " + e.Launcher + " is only its launcher"
			}
			if unreadable {
				return "component unreadable", "component_unreadable", e.Name + " source could not be read; upgrading " + e.Launcher + " does not repair that component"
			}
			return "component unavailable", "component_unavailable", e.Launcher + " exists, but " + e.Name + " could not be found or loaded in the probed shell context"
		}
		if e.ComponentKind == "hosted" {
			return "component unavailable", "component_unavailable", e.Launcher + " exists, but the hosted component did not return its expected version"
		}
		if len(v.Errors) > 0 {
			return "probe failed", "probe_failed", "The selected manager's version probe failed; inspect its reported error"
		}
		return "version unknown", "parser_mismatch", "The selected executable returned no version matching the pinned backend parser"
	}
	if !v.Fresh {
		return "version unsupported", "version_unsupported", e.Reason
	}
	return "unavailable", "probe_failed", "The selected manager is not available in the current context"
}
