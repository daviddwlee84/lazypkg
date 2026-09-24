// Package catalog embeds static metadata generated from the pinned mpm release.
// Runtime discovery supplies availability; the catalog never executes Python.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type Entry struct {
	ID             string   `json:"id"`
	BackendID      string   `json:"backend_id"`
	Name           string   `json:"name"`
	Requirement    string   `json:"requirement"`
	Capabilities   []string `json:"capabilities"`
	Platforms      []string `json:"platforms"`
	CLINames       []string `json:"cli_names"`
	Keywords       []string `json:"keywords"`
	Scope          string   `json:"scope"`
	Groups         []string `json:"groups"`
	Maintained     bool     `json:"maintained"`
	Maintenance    string   `json:"maintenance"`
	SourceURL      string   `json:"source_url"`
	Reason         string   `json:"reason"`
	ComponentKind  string   `json:"component_kind"`
	VersionSubject string   `json:"version_subject"`
	Launcher       string   `json:"launcher"`
}

//go:embed catalog.json
var artifact []byte

var entries []Entry
var byID map[string]Entry

func init() {
	var data struct {
		Schema   int     `json:"schema_version"`
		Version  string  `json:"backend_version"`
		Managers []Entry `json:"managers"`
	}
	if err := json.Unmarshal(artifact, &data); err != nil {
		panic(fmt.Sprintf("invalid embedded catalog: %v", err))
	}
	if data.Schema != 1 || data.Version != domain.MPMVersion {
		panic("catalog and backend versions differ; regenerate the catalog")
	}
	entries = data.Managers
	byID = make(map[string]Entry, len(entries))
	for _, e := range entries {
		if _, ok := byID[e.ID]; ok {
			panic("duplicate catalog ID: " + e.ID)
		}
		byID[e.ID] = e
	}
}

func clone(e Entry) Entry {
	e.Capabilities = append([]string(nil), e.Capabilities...)
	e.Platforms = append([]string(nil), e.Platforms...)
	e.CLINames = append([]string(nil), e.CLINames...)
	e.Keywords = append([]string(nil), e.Keywords...)
	e.Groups = append([]string(nil), e.Groups...)
	return e
}

func All() []Entry {
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = clone(e)
	}
	return out
}
func Normalize(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "uv" {
		return "uvx"
	}
	return id
}
func Lookup(id string) (Entry, bool) { e, ok := byID[Normalize(id)]; return clone(e), ok }
func BackendID(id string) string {
	if e, ok := Lookup(id); ok {
		return e.BackendID
	}
	return Normalize(id)
}
func Groups() map[string][]string {
	groups := map[string][]string{}
	for _, e := range entries {
		groups["all"] = append(groups["all"], e.ID)
		for _, g := range e.Groups {
			groups[g] = append(groups[g], e.ID)
		}
	}
	for k := range groups {
		sort.Strings(groups[k])
	}
	return groups
}
func DefaultIDs() []string {
	out := []string{}
	for _, e := range entries {
		if e.Maintained && e.Scope == "global" {
			out = append(out, e.ID)
		}
	}
	return out
}
