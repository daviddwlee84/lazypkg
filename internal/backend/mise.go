package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type Mise struct {
	Path    string
	Dir     string
	Runner  process.Runner
	Env     map[string]string
	Timeout time.Duration
}

func (m Mise) Command(args ...string) domain.Command {
	return domain.Command{Path: m.Path, Args: args, Env: m.Env, Dir: m.Dir}
}
func (m Mise) output(ctx context.Context, args ...string) (process.Result, error) {
	d := m.Timeout
	if d == 0 {
		d = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return m.Runner.Output(ctx, m.Command(args...))
}

// A --global listing still resolves tools in the current project first. Read
// from a clean context and stop ancestor discovery, keeping global config and
// MISE_ENV but excluding shell tool-version overrides from the global view.
func (m Mise) globalOutput(ctx context.Context, args ...string) (process.Result, error) {
	dir, err := os.MkdirTemp("", "lazypkg-mise-global-*")
	if err != nil {
		return process.Result{}, err
	}
	defer os.RemoveAll(dir)
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return process.Result{}, err
	}
	c := m.Command(args...)
	c.Dir = canonical
	c.Env = map[string]string{}
	for k, v := range m.Env {
		c.Env[k] = v
	}
	for _, entry := range process.Environment(nil, c) {
		k, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "MISE_") && strings.HasSuffix(upper, "_VERSION") && len(upper) > len("MISE__VERSION") {
			c.Unset = append(c.Unset, k)
			delete(c.Env, k)
		}
	}
	c.Env["MISE_CEILING_PATHS"] = canonical
	d := m.Timeout
	if d <= 0 {
		d = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return m.Runner.Output(ctx, c)
}

type miseEntry struct {
	Version   string `json:"version"`
	Path      string `json:"install_path"`
	Installed *bool  `json:"installed"`
	Active    bool   `json:"active"`
	Requested string `json:"requested_version"`
	Source    *struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
}

func decodeMise(b string) (map[string][]miseEntry, error) {
	var data map[string][]miseEntry
	err := json.Unmarshal([]byte(b), &data)
	if err == nil && data == nil {
		err = fmt.Errorf("mise returned null instead of an object")
	}
	return data, err
}
func (m Mise) Installed(ctx context.Context) (domain.Snapshot, error) {
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	r, err := m.output(ctx, "ls", "--installed", "--json")
	if err != nil {
		return s, err
	}
	all, err := decodeMise(r.Stdout)
	if err != nil {
		return s, err
	}
	global := map[string]bool{}
	current := map[string]miseEntry{}
	for _, mode := range []string{"--global", "--current"} {
		var r process.Result
		var e error
		if mode == "--global" {
			r, e = m.globalOutput(ctx, "ls", mode, "--json")
		} else {
			r, e = m.output(ctx, "ls", mode, "--json")
		}
		if e != nil {
			s.Issues = append(s.Issues, domain.Issue{Manager: "mise", Message: e.Error()})
			continue
		}
		data, e := decodeMise(r.Stdout)
		if e != nil {
			s.Issues = append(s.Issues, domain.Issue{Manager: "mise", Message: e.Error()})
			continue
		}
		for id, entries := range data {
			for _, v := range entries {
				key := id + "@" + v.Version
				if mode == "--global" {
					global[key] = true
				} else {
					current[key] = v
				}
			}
		}
	}
	for id, entries := range all {
		for _, v := range entries {
			if v.Installed != nil && !*v.Installed {
				continue
			}
			p := domain.Package{Manager: "mise", ID: id, Name: id, Version: v.Version, Scope: "user runtime", Root: v.Path, Global: global[id+"@"+v.Version], Evidence: []domain.Evidence{{Kind: "recorded", Source: "mise ls --installed --json", Detail: "Installed runtime; configuration selection is independent of PATH"}}}
			if c, ok := current[id+"@"+v.Version]; ok {
				p.Active = true
				if c.Source != nil {
					p.ConfigSource = c.Source.Path
				}
			}
			s.Packages = append(s.Packages, p)
		}
	}
	return s, nil
}
func (m Mise) Latest(ctx context.Context, id, version string) (string, error) {
	if version != "" {
		id += "@" + version
	}
	r, err := m.output(ctx, "latest", id)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(r.Stdout)
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return "", fmt.Errorf("invalid mise version response %q", v)
	}
	return v, nil
}

// Outdated filters to versions selected globally; project configuration is never a mutation target.
func (m Mise) Outdated(ctx context.Context) (domain.Snapshot, error) {
	s, err := m.Installed(ctx)
	if err != nil {
		return s, err
	}
	out := domain.Snapshot{Packages: []domain.Package{}, Issues: s.Issues, ObservedAt: s.ObservedAt}
	r, err := m.globalOutput(ctx, "outdated", "--json", "--bump")
	if err != nil {
		return out, err
	}
	var updates map[string]struct {
		Current string `json:"current"`
		Latest  string `json:"latest"`
	}
	if err = json.Unmarshal([]byte(r.Stdout), &updates); err != nil {
		return out, err
	}
	if updates == nil {
		return out, fmt.Errorf("mise outdated returned null instead of an object")
	}
	installed := map[string]bool{}
	for _, p := range s.Packages {
		installed[p.ID+"@"+p.Version] = true
	}
	for _, p := range s.Packages {
		if !p.Global {
			continue
		}
		v, ok := updates[p.ID]
		if ok && v.Current == p.Version && v.Latest != "" && v.Latest != p.Version {
			p.Latest = v.Latest
			p.LatestInstalled = installed[p.ID+"@"+v.Latest]
			p.Description = "New release available; installing it retains the old version. Global activation is separate."
			out.Packages = append(out.Packages, p)
		}
	}
	return out, nil
}
