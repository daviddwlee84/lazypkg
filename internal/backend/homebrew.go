package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// Homebrew reconciles mpm's short installed IDs with Homebrew's tap-qualified
// update/catalog IDs. Installed and catalog meaning deliberately use separate
// indexes: an installed tap's foo cannot establish the identity of core foo.
type Homebrew struct {
	Path    string
	Runner  process.Runner
	Env     map[string]string
	Timeout time.Duration
}

const homebrewIdentityChunk = 64

var hbName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@+._-]*$`)
var hbTapPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func hbTap(s string) bool {
	p := strings.Split(s, "/")
	return len(p) == 2 && hbTapPart.MatchString(p[0]) && hbTapPart.MatchString(p[1])
}
func hbID(s string) bool {
	p := strings.Split(s, "/")
	return len(p) == 1 && hbName.MatchString(s) || len(p) == 3 && hbTap(p[0]+"/"+p[1]) && hbName.MatchString(p[2])
}
func hbKind(manager string) (kind, core, flag string, err error) {
	switch manager {
	case "brew":
		return "formula", "homebrew/core", "--formula", nil
	case "cask":
		return "cask", "homebrew/cask", "--cask", nil
	}
	return "", "", "", fmt.Errorf("%s is not a Homebrew provider", manager)
}
func (h *Homebrew) output(ctx context.Context, args ...string) (process.Result, error) {
	env := readOnlyEnv(h.Env)
	for k, v := range map[string]string{"HOMEBREW_NO_AUTO_UPDATE": "1", "HOMEBREW_NO_INSTALL_CLEANUP": "1", "HOMEBREW_NO_AUTOREMOVE": "1", "HOMEBREW_NO_ANALYTICS": "1", "HOMEBREW_NO_ENV_HINTS": "1", "NO_COLOR": "1"} {
		env[k] = v
	}
	r := h.Runner
	if r == nil {
		r = process.ExecRunner{}
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.Output(work, domain.Command{Path: h.Path, Args: args, Env: env})
}

type hbFormula struct {
	Name      string   `json:"name"`
	FullName  string   `json:"full_name"`
	Tap       string   `json:"tap"`
	Aliases   []string `json:"aliases"`
	OldNames  []string `json:"oldnames"`
	Installed []struct {
		Version string `json:"version"`
	} `json:"installed"`
}
type hbCask struct {
	Token     string          `json:"token"`
	FullToken string          `json:"full_token"`
	Tap       string          `json:"tap"`
	OldTokens []string        `json:"old_tokens"`
	Installed json.RawMessage `json:"installed"`
}
type hbPayload struct {
	Formulae []hbFormula `json:"formulae"`
	Casks    []hbCask    `json:"casks"`
}

func hbDecode(text string) (hbPayload, error) {
	var p hbPayload
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil || raw == nil {
		return p, errors.New("Homebrew identity metadata is not a JSON object")
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return p, errors.New("invalid Homebrew identity metadata fields")
	}
	if _, ok := raw["formulae"]; !ok {
		if _, ok := raw["casks"]; !ok {
			return p, errors.New("Homebrew metadata has no formulae or casks section")
		}
	}
	return p, nil
}

type hbRecord struct {
	identity domain.PackageIdentity
	versions map[string]bool
}
type HomebrewIndex struct {
	manager   string
	records   map[string]*hbRecord
	aliases   map[string]map[string]bool
	installed bool
}

func newHBIndex(manager string, installed bool) *HomebrewIndex {
	return &HomebrewIndex{manager: manager, records: map[string]*hbRecord{}, aliases: map[string]map[string]bool{}, installed: installed}
}
func hbClone(x domain.PackageIdentity) domain.PackageIdentity {
	x.Aliases = append([]string(nil), x.Aliases...)
	return x
}
func hbUnique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range values {
		s = strings.ToLower(s)
		if hbID(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
func hbMetadataIdentity(manager, name, full, tap string, aliases []string) domain.PackageIdentity {
	kind, core, _, _ := hbKind(manager)
	name = strings.ToLower(name)
	full = strings.ToLower(full)
	tap = strings.ToLower(tap)
	x := domain.PackageIdentity{State: "unresolved", CanonicalID: full, Name: name, Tap: tap, Kind: kind, Source: "brew info --json=v2"}
	if !hbName.MatchString(name) || !hbTap(tap) || !hbID(full) {
		x.Reason = "Homebrew did not report a complete package/tap identity"
		return x
	}
	canonical := tap + "/" + name
	if tap == core {
		canonical = name
	}
	if full != canonical && !(tap == core && full == tap+"/"+name) {
		x.Reason = "Homebrew full name and tap metadata disagree"
		return x
	}
	x.CanonicalID = canonical
	x.State = "verified"
	values := []string{canonical, name, tap + "/" + name}
	for _, alias := range aliases {
		alias = strings.ToLower(alias)
		if hbName.MatchString(alias) {
			values = append(values, alias, tap+"/"+alias)
		} else if strings.HasPrefix(alias, tap+"/") && hbID(alias) {
			values = append(values, alias, strings.TrimPrefix(alias, tap+"/"))
		}
	}
	x.Aliases = hbUnique(values)
	return x
}
func (i *HomebrewIndex) add(r hbRecord) {
	key := r.identity.CanonicalID
	if !hbID(key) {
		key = "unresolved:" + r.identity.Name
	}
	if old := i.records[key]; old != nil {
		if old.identity.State != r.identity.State || old.identity.Tap != r.identity.Tap || old.identity.Name != r.identity.Name {
			old.identity.State = "ambiguous"
			old.identity.Reason = "Conflicting Homebrew metadata describes this package"
		}
		old.identity.Aliases = hbUnique(append(old.identity.Aliases, r.identity.Aliases...))
		for v := range r.versions {
			old.versions[v] = true
		}
	} else {
		copy := r
		i.records[key] = &copy
	}
	rr := i.records[key]
	for _, alias := range hbUnique(append(append([]string(nil), rr.identity.Aliases...), rr.identity.Name, rr.identity.CanonicalID)) {
		if i.aliases[alias] == nil {
			i.aliases[alias] = map[string]bool{}
		}
		i.aliases[alias][key] = true
	}
}
func (i *HomebrewIndex) resolve(id string) (*hbRecord, domain.PackageIdentity, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	kind, _, _, _ := hbKind(i.manager)
	unknown := domain.PackageIdentity{State: "unresolved", Kind: kind, Source: "Homebrew identity metadata", Reason: "Package identity was not found in the requested Homebrew metadata"}
	if !hbID(id) {
		unknown.Reason = "Invalid Homebrew package identifier"
		return nil, unknown, errors.New(unknown.Reason)
	}
	keys := i.aliases[id]
	if len(keys) == 0 {
		return nil, unknown, errors.New(unknown.Reason)
	}
	if len(keys) != 1 {
		unknown.State = "ambiguous"
		unknown.Reason = "This spelling identifies more than one Homebrew package; select its complete tap-qualified name"
		return nil, unknown, errors.New(unknown.Reason)
	}
	for key := range keys {
		r := i.records[key]
		x := hbClone(r.identity)
		if x.State != "verified" {
			return r, x, errors.New(x.Reason)
		}
		return r, x, nil
	}
	return nil, unknown, errors.New(unknown.Reason)
}
func (i *HomebrewIndex) Resolve(id string) (domain.PackageIdentity, error) {
	_, x, err := i.resolve(id)
	return x, err
}

type hbReceipt struct {
	Source struct {
		Tap     string `json:"tap"`
		Version string `json:"version"`
	} `json:"source"`
}

func hbReadReceipt(path, root string) (hbReceipt, error) {
	var receipt hbReceipt
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return receipt, errors.New("installation receipt is missing or unreadable")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return receipt, errors.New("installation root is unreadable")
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return receipt, errors.New("installation receipt resolves outside the selected Homebrew root")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return receipt, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
		return receipt, errors.New("installation receipt is not a bounded regular file")
	}
	if json.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&receipt) != nil {
		return receipt, errors.New("invalid installation receipt")
	}
	if !hbTap(receipt.Source.Tap) {
		return receipt, errors.New("installation receipt does not identify its tap")
	}
	return receipt, nil
}
func hbVersionPath(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\\x00\r\n\x1b")
}
func hbReceiptIdentity(r *hbRecord, root, manager string) {
	if r.identity.State != "verified" {
		return
	}
	if len(r.versions) == 0 {
		r.identity.State = "unresolved"
		r.identity.Reason = "Homebrew reported no installed version for this identity"
		return
	}
	for version := range r.versions {
		if !hbVersionPath(version) {
			r.identity.State = "unresolved"
			r.identity.Reason = "Invalid installed version in Homebrew metadata"
			return
		}
		path := filepath.Join(root, r.identity.Name, version, "INSTALL_RECEIPT.json")
		if manager == "cask" {
			path = filepath.Join(root, r.identity.Name, ".metadata", "INSTALL_RECEIPT.json")
		}
		receipt, err := hbReadReceipt(path, root)
		if err != nil {
			r.identity.State = "unresolved"
			r.identity.Reason = err.Error()
			return
		}
		if !strings.EqualFold(receipt.Source.Tap, r.identity.Tap) {
			r.identity.State = "ambiguous"
			r.identity.Reason = "Installed receipt names tap " + receipt.Source.Tap + " but current metadata names " + r.identity.Tap + "; native review is required"
			return
		}
		if manager == "cask" && receipt.Source.Version != version {
			r.identity.State = "unresolved"
			r.identity.Reason = "Cask receipt and installed version metadata disagree"
			return
		}
	}
	r.identity.Source = "brew info --json=v2 --installed and installation receipt"
}
func hbRecords(p hbPayload, manager string) []hbRecord {
	out := []hbRecord{}
	if manager == "brew" {
		for _, f := range p.Formulae {
			r := hbRecord{identity: hbMetadataIdentity(manager, f.Name, f.FullName, f.Tap, append(append([]string(nil), f.Aliases...), f.OldNames...)), versions: map[string]bool{}}
			for _, v := range f.Installed {
				r.versions[v.Version] = true
			}
			out = append(out, r)
		}
	} else {
		for _, c := range p.Casks {
			r := hbRecord{identity: hbMetadataIdentity(manager, c.Token, c.FullToken, c.Tap, c.OldTokens), versions: map[string]bool{}}
			var version string
			if json.Unmarshal(c.Installed, &version) == nil && version != "" {
				r.versions[version] = true
			}
			out = append(out, r)
		}
	}
	return out
}
func (h *Homebrew) InstalledIndex(ctx context.Context, manager string) (*HomebrewIndex, error) {
	_, _, flag, err := hbKind(manager)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := h.output(ctx, "info", "--json=v2", "--installed", flag)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Homebrew installed identity metadata could not be read")
	}
	payload, err := hbDecode(r.Stdout)
	if err != nil {
		return nil, err
	}
	option := "--cellar"
	if manager == "cask" {
		option = "--caskroom"
	}
	where, err := h.output(ctx, option)
	root := strings.TrimSpace(where.Stdout)
	if err != nil || !filepath.IsAbs(root) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Homebrew returned no verified absolute installation root")
	}
	index := newHBIndex(manager, true)
	for _, record := range hbRecords(payload, manager) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hbReceiptIdentity(&record, root, manager)
		index.add(record)
	}
	return index, nil
}

func hbApply(p *domain.Package, x domain.PackageIdentity) {
	copy := hbClone(x)
	p.Identity = &copy
	if x.State == "verified" {
		p.ID = x.CanonicalID
		if p.Name == "" {
			p.Name = x.Name
		}
	}
}
func hbIssue(s *domain.Snapshot, p domain.Package, x domain.PackageIdentity) {
	s.Issues = append(s.Issues, domain.Issue{Manager: p.Manager, PackageID: p.ID, Kind: "identity", Message: p.ID + ": " + x.Reason})
}
func (i *HomebrewIndex) Normalize(snapshot domain.Snapshot) domain.Snapshot {
	s := domain.CloneSnapshot(snapshot)
	for n := range s.Packages {
		p := &s.Packages[n]
		if p.Manager != i.manager {
			continue
		}
		record, x, err := i.resolve(p.ID)
		if err == nil && i.installed && p.Version != "" && !record.versions[p.Version] {
			x.State = "unresolved"
			x.Reason = "Reported installed version does not match the verified Homebrew metadata"
			err = errors.New(x.Reason)
		}
		hbApply(p, x)
		if err != nil {
			hbIssue(&s, *p, x)
		}
	}
	return s
}

type hbCatalogFailure struct{ missing bool }

func (e hbCatalogFailure) Error() string { return "Homebrew catalog identity lookup failed" }
func (h *Homebrew) catalogIndex(ctx context.Context, manager string, selectors []string) (*HomebrewIndex, error) {
	_, _, flag, err := hbKind(manager)
	if err != nil {
		return nil, err
	}
	args := []string{"info", "--json=v2", flag}
	args = append(args, selectors...)
	r, err := h.output(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stderr := strings.ToLower(r.Stderr)
		missing := strings.Contains(stderr, "no available formula with the name") || strings.Contains(stderr, "no available cask with the name")
		return nil, hbCatalogFailure{missing: missing}
	}
	p, err := hbDecode(r.Stdout)
	if err != nil {
		return nil, err
	}
	index := newHBIndex(manager, false)
	for _, record := range hbRecords(p, manager) {
		index.add(record)
	}
	return index, nil
}
func (h *Homebrew) Catalog(ctx context.Context, manager, query string, snapshot domain.Snapshot) (domain.Snapshot, error) {
	s := domain.CloneSnapshot(snapshot)
	kind, core, _, err := hbKind(manager)
	if err != nil {
		return s, err
	}
	bySelector := map[string][]int{}
	selectors := []string{}
	count := 0
	for _, p := range s.Packages {
		if p.Manager == manager {
			count++
		}
	}
	for n, p := range s.Packages {
		if p.Manager != manager {
			continue
		}
		raw := strings.ToLower(p.ID)
		selector := raw
		if !hbID(raw) {
			x := domain.PackageIdentity{State: "unresolved", Kind: kind, Reason: "Invalid Homebrew catalog identifier"}
			hbApply(&s.Packages[n], x)
			hbIssue(&s, p, x)
			continue
		}
		if !strings.Contains(raw, "/") {
			if hbID(query) && strings.Count(query, "/") == 2 {
				if count != 1 {
					x := domain.PackageIdentity{State: "unresolved", Kind: kind, Reason: "A tap-qualified search returned multiple unqualified IDs"}
					hbApply(&s.Packages[n], x)
					hbIssue(&s, p, x)
					continue
				}
				selector = strings.ToLower(query)
			} else {
				selector = core + "/" + raw
			}
		}
		if _, ok := bySelector[selector]; !ok {
			selectors = append(selectors, selector)
		}
		bySelector[selector] = append(bySelector[selector], n)
	}
	sort.Strings(selectors)
	for start := 0; start < len(selectors); start += homebrewIdentityChunk {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		end := min(start+homebrewIdentityChunk, len(selectors))
		index, lookupErr := h.catalogIndex(ctx, manager, selectors[start:end])
		for _, selector := range selectors[start:end] {
			var x domain.PackageIdentity
			var e error
			if lookupErr != nil {
				x = domain.PackageIdentity{State: "unresolved", Kind: kind, Source: "brew info --json=v2 catalog", Reason: "Catalog identity could not be verified for this metadata batch"}
				e = lookupErr
			} else {
				x, e = index.Resolve(selector)
			}
			for _, n := range bySelector[selector] {
				hbApply(&s.Packages[n], x)
				if e != nil {
					hbIssue(&s, s.Packages[n], x)
				}
			}
		}
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
	}
	return s, nil
}
func (h *Homebrew) ResolveCatalog(ctx context.Context, manager, id string) (domain.PackageIdentity, error) {
	_, core, _, err := hbKind(manager)
	if err != nil {
		return domain.PackageIdentity{}, err
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if !hbID(id) {
		return domain.PackageIdentity{State: "unresolved", Reason: "Invalid Homebrew catalog identifier"}, errors.New("invalid Homebrew catalog identifier")
	}
	selector := id
	if !strings.Contains(id, "/") {
		selector = core + "/" + id
	}
	index, err := h.catalogIndex(ctx, manager, []string{selector})
	if err != nil && !strings.Contains(id, "/") {
		var failure hbCatalogFailure
		if errors.As(err, &failure) && failure.missing {
			index, err = h.catalogIndex(ctx, manager, []string{id})
			selector = id
		}
	}
	if err != nil {
		return domain.PackageIdentity{State: "unresolved", Source: "brew info --json=v2 catalog", Reason: "The requested catalog identity could not be resolved"}, err
	}
	return index.Resolve(selector)
}
