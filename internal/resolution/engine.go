// Package resolution prepares individually reviewed removals of verified
// duplicate installations. Unknown ownership and unbounded effects are guidance.
package resolution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type Engine struct {
	Runner process.Runner
	Env    map[string]string
	Dir    string
	GOOS   string
	Fresh  func(context.Context, string) (domain.ConflictAssessment, error)
}

func New(r process.Runner) *Engine {
	if r == nil {
		r = process.ExecRunner{}
	}
	dir, _ := os.Getwd()
	return &Engine{Runner: r, Dir: dir, GOOS: runtime.GOOS}
}

func (e *Engine) getenv(key string) string {
	if value, ok := e.Env[key]; ok {
		return value
	}
	return os.Getenv(key)
}

func (e *Engine) command(path string, args ...string) domain.Command {
	env := map[string]string{}
	for key, value := range e.Env {
		env[key] = value
	}
	for key, value := range map[string]string{"LC_ALL": "C", "LANG": "C", "NO_COLOR": "1", "HOMEBREW_NO_AUTO_UPDATE": "1", "HOMEBREW_NO_ANALYTICS": "1", "HOMEBREW_NO_INSTALL_CLEANUP": "1", "HOMEBREW_NO_AUTOREMOVE": "1", "MISE_AUTO_INSTALL": "0", "MISE_NOT_FOUND_AUTO_INSTALL": "false", "GOTOOLCHAIN": "local"} {
		env[key] = value
	}
	return domain.Command{Path: path, Args: args, Env: env, Dir: e.Dir}
}

func (e *Engine) output(ctx context.Context, c domain.Command) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := e.Runner.Output(ctx, c)
	return r.Stdout, err
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func canonical(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(path)
}

func within(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(canonical(root), canonical(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func identity(path string) string {
	s, err := os.Stat(path)
	if err != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%d:%d:%d", canonical(path), s.Size(), s.Mode(), s.ModTime().UnixNano())
}

func readJSON(path string, into any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(into)
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (4<<20)+1))
	if err != nil || n > 4<<20 {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}

func unique(values []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !m[value] {
			m[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func blocked(i *domain.ConflictInstallation, reason string) {
	i.Status = "blocked"
	i.Blockers = append(i.Blockers, reason)
}

func projectURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "git+")
	if strings.HasPrefix(raw, "git@") {
		raw = "https://" + strings.Replace(strings.TrimPrefix(raw, "git@"), ":", "/", 1)
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" {
		return ""
	}
	for _, host := range []string{"github.com", "gitlab.com", "codeberg.org"} {
		if strings.EqualFold(u.Host, host) && (u.Scheme == "https" || u.Scheme == "ssh" || u.Scheme == "git" || u.Scheme == "http") {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				return host + "/" + strings.ToLower(parts[0]) + "/" + strings.ToLower(strings.TrimSuffix(strings.Split(parts[1], "#")[0], ".git"))
			}
		}
	}
	return ""
}

func managerByID(managers []domain.Manager, id string) domain.Manager {
	for _, m := range managers {
		if m.ID == id {
			return m
		}
	}
	return domain.Manager{ID: id}
}

func (e *Engine) finish(i *domain.ConflictInstallation, binding ...string) {
	i.Commands = unique(i.Commands)
	i.Dependents = unique(i.Dependents)
	i.Blockers = unique(i.Blockers)
	i.Warnings = unique(i.Warnings)
	keys := []string{i.Package.Key(), canonical(i.Package.Root), i.ManagerPath, i.RuntimePath, i.Prefix}
	if i.Package.Manager == "" {
		for _, path := range i.Paths {
			keys = append(keys, canonical(path.Target), canonical(path.Path))
		}
	}
	i.ID = digest(keys)[:20]
	artifacts := []string{}
	for _, path := range i.Paths {
		artifacts = append(artifacts, identity(path.Path), identity(path.Target))
	}
	artifacts = unique(artifacts)
	i.ArtifactFingerprint = digest(artifacts)
	binding = append(binding, artifacts...)
	binding = append(binding, identity(i.ManagerPath), identity(i.RuntimePath), e.Dir, e.getenv("PATH"))
	i.Fingerprint = ""
	i.Fingerprint = digest([]any{*i, binding})
}

func executable(path string, goos string) bool {
	s, err := os.Stat(path)
	return err == nil && s.Mode().IsRegular() && (goos == "windows" || s.Mode()&0111 != 0)
}
