package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type queryDisk struct {
	Schema   int             `json:"schema"`
	Context  string          `json:"context"`
	Kind     string          `json:"kind"`
	Manager  domain.Manager  `json:"manager"`
	Snapshot domain.Snapshot `json:"snapshot"`
}

func digestJSON(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Only the hash of environment values is persisted. Namespace settings prevent
// one global prefix or runtime context from borrowing another's inventory.
func (a *App) queryContext() string {
	dir, _ := os.Getwd()
	values := map[string]string{"cwd": dir, "platform": runtime.GOOS + "/" + runtime.GOARCH, "mpm_version": domain.MPMVersion, "mpm_path": a.settings().MPMPath}
	for _, entry := range process.Environment(nil, domain.Command{Env: a.childEnv()}) {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(k)
		if upper == "PATH" || upper == "PATHEXT" || upper == "HOME" || upper == "USERPROFILE" || upper == "GOPATH" || upper == "GOBIN" || upper == "GOTOOLCHAIN" || upper == "SCOOP" || upper == "CHOCOLATEYINSTALL" || strings.HasPrefix(upper, "MISE_") || strings.HasPrefix(upper, "UV_") || strings.HasPrefix(upper, "NPM_CONFIG_") || strings.HasPrefix(upper, "GEM_") || strings.HasPrefix(upper, "BUNDLE_") || strings.HasPrefix(upper, "CARGO_") || strings.HasPrefix(upper, "RUSTUP_") || strings.HasPrefix(upper, "XDG_") {
			values[upper] = v
		}
	}
	return digestJSON(values)
}

func (a *App) queryDiskDir() string {
	c := a.settings()
	if c.CacheDir == "" || c.QueryCache != nil && !*c.QueryCache {
		return ""
	}
	return filepath.Join(c.CacheDir, "queries-v1")
}
func (a *App) queryDiskPath(contextKey, kind, id string) string {
	dir := a.queryDiskDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, digestJSON([]string{contextKey, kind, id})+".json")
}
func (a *App) loadQueryDisk(contextKey, kind, id string) (queryDisk, bool) {
	var record queryDisk
	path := a.queryDiskPath(contextKey, kind, id)
	if path == "" {
		return record, false
	}
	f, err := os.Open(path)
	if err != nil {
		return record, false
	}
	defer f.Close()
	if err = json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&record); err != nil {
		return record, false
	}
	if record.Schema != 1 || record.Context != contextKey || record.Kind != kind || record.Manager.ID != id || len(record.Snapshot.Coverage) != 1 {
		return record, false
	}
	c := record.Snapshot.Coverage[0]
	age := time.Since(c.ObservedAt)
	if c.Manager != id || c.State != "complete" || c.ObservedAt.IsZero() || age < 0 || age >= 24*time.Hour {
		return record, false
	}
	for _, p := range record.Snapshot.Packages {
		if p.Manager != id {
			return queryDisk{}, false
		}
	}
	return record, true
}

// Caller holds diskMu; epoch is checked immediately before atomic publication.
func (a *App) saveQueryDisk(contextKey, kind string, m domain.Manager, s domain.Snapshot, epoch uint64) {
	path := a.queryDiskPath(contextKey, kind, m.ID)
	if path == "" || len(s.Coverage) != 1 || s.Coverage[0].State != "complete" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	data, err := json.Marshal(queryDisk{1, contextKey, kind, m, s})
	if err != nil || len(data) > 8<<20 {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".query-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_ = f.Chmod(0600)
	_, err = f.Write(data)
	closed := f.Close()
	if err != nil || closed != nil {
		return
	}
	a.cacheMu.Lock()
	valid := epoch == a.cacheEpoch
	a.cacheMu.Unlock()
	if valid {
		_ = os.Rename(f.Name(), path)
	}
}
func (a *App) clearQueryDisk() {
	dir := a.queryDiskDir()
	if dir == "" {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
