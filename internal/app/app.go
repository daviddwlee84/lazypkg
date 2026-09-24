package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/bootstrap"
	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/diagnostics"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type App struct {
	Config     config.Config
	Runner     process.Runner
	Bootstrap  *bootstrap.Engine
	HTTPClient *http.Client
	mu         sync.Mutex
	mpm        *backend.MPM
	writeMu    sync.Mutex
}

func New(c config.Config) *App {
	r := process.ExecRunner{}
	return &App{Config: c, Runner: r, Bootstrap: bootstrap.New(r, c.DataDir)}
}
func (a *App) provider(ctx context.Context) (*backend.MPM, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	path := a.Config.MPMPath
	if path == "" {
		var err error
		path, err = a.Bootstrap.ResolveMPM(ctx)
		if err != nil {
			return nil, fmt.Errorf("mpm backend unavailable: %w; run lazypkg setup", err)
		}
	}
	if a.mpm == nil || a.mpm.Path != path {
		a.mpm = &backend.MPM{Path: path, Runner: a.Runner, Timeout: a.Config.Timeout(), Env: a.Bootstrap.ChildEnv()}
	}
	return a.mpm, nil
}
func (a *App) Managers(ctx context.Context) ([]domain.Manager, error) {
	for _, id := range a.Config.Managers {
		if !backend.Known(backend.NormalizeManager(id)) {
			return nil, fmt.Errorf("configuration contains unsupported manager %q", id)
		}
	}
	m, err := a.provider(ctx)
	if err != nil {
		return nil, err
	}
	return m.Managers(ctx)
}
func (a *App) mise() (backend.Mise, error) {
	path, err := a.Bootstrap.Lookup("mise")
	return backend.Mise{Path: path, Runner: a.Runner, Env: a.Bootstrap.ChildEnv(), Timeout: a.Config.Timeout()}, err
}
func (a *App) enabled(id string) bool {
	if len(a.Config.Managers) == 0 {
		return true
	}
	for _, m := range a.Config.Managers {
		if backend.NormalizeManager(m) == id {
			return true
		}
	}
	return false
}
func (a *App) Packages(ctx context.Context, kind, query, manager string) (domain.Snapshot, error) {
	return a.packages(ctx, kind, query, manager, true)
}
func (a *App) packages(ctx context.Context, kind, query, manager string, enrich bool) (domain.Snapshot, error) {
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	if kind != "installed" && kind != "search" && kind != "outdated" {
		return s, fmt.Errorf("unknown query %q", kind)
	}
	manager = backend.NormalizeManager(manager)
	if manager != "" && !backend.Known(manager) {
		return s, fmt.Errorf("unsupported manager %q", manager)
	}
	if kind == "search" {
		query = strings.TrimSpace(query)
		if query == "" {
			return s, fmt.Errorf("search query is required")
		}
		if strings.ContainsAny(query, "\x00\r\n\"`$;&|<>%!") {
			return s, fmt.Errorf("query contains shell metacharacters unsupported by downstream managers")
		}
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return s, err
	}
	mpm, err := a.provider(ctx)
	if err != nil {
		return s, err
	}
	targets := []domain.Manager{}
	found := false
	for _, m := range managers {
		if manager != "" && manager != m.ID {
			continue
		}
		if manager == "" && !a.enabled(m.ID) {
			continue
		}
		found = true
		if !m.Available {
			if manager != "" || m.Path != "" {
				s.Issues = append(s.Issues, domain.Issue{Manager: m.ID, Message: m.Status + ": " + strings.Join(m.Errors, "; ")})
			}
			continue
		}
		if !m.Supports(kind) {
			s.Issues = append(s.Issues, domain.Issue{Manager: m.ID, Message: "does not support " + kind})
			continue
		}
		targets = append(targets, m)
	}
	if manager != "" && !found {
		return s, fmt.Errorf("%s is not supported on this platform", manager)
	}
	type reply struct {
		s   domain.Snapshot
		err error
		id  string
	}
	ch := make(chan reply, len(targets))
	sem := make(chan struct{}, 4)
	for _, m := range targets {
		go func(m domain.Manager) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				ch <- reply{err: ctx.Err(), id: m.ID}
				return
			}
			defer func() { <-sem }()
			var r domain.Snapshot
			var err error
			if m.ID == "mise" && kind != "search" {
				var mi backend.Mise
				mi, err = a.mise()
				if err == nil {
					if kind == "installed" {
						r, err = mi.Installed(ctx)
					} else {
						r, err = mi.Outdated(ctx)
					}
				}
			} else if m.ID == "uvx" && kind == "search" {
				r, err = backend.PyPIExact(ctx, a.HTTPClient, "", query)
			} else {
				backendQuery := ""
				if kind == "search" {
					backendQuery = query
				}
				r, err = mpm.Packages(ctx, kind, backendQuery, m.ID)
			}
			ch <- reply{s: r, err: err, id: m.ID}
		}(m)
	}
	for range targets {
		r := <-ch
		if r.err != nil {
			s.Issues = append(s.Issues, domain.Issue{Manager: r.id, Message: r.err.Error()})
		}
		s.Packages = append(s.Packages, r.s.Packages...)
		s.Issues = append(s.Issues, r.s.Issues...)
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if kind == "installed" && enrich {
		engine := a.diagnosticEngine()
		var issues []domain.Issue
		s.Packages, issues = engine.Enrich(ctx, s.Packages)
		s.Issues = append(s.Issues, issues...)
	}
	if kind != "search" && query != "" {
		filtered := s.Packages[:0]
		q := strings.ToLower(query)
		for _, p := range s.Packages {
			if strings.Contains(strings.ToLower(p.ID+" "+p.Name+" "+strings.Join(p.Commands, " ")), q) {
				filtered = append(filtered, p)
			}
		}
		s.Packages = filtered
	}
	sort.Slice(s.Packages, func(i, j int) bool {
		p, q := s.Packages[i], s.Packages[j]
		if p.ID != q.ID {
			return p.ID < q.ID
		}
		if p.Manager != q.Manager {
			return p.Manager < q.Manager
		}
		return p.Version < q.Version
	})
	return s, nil
}
func (a *App) Diagnose(ctx context.Context, name string) (domain.DiagnosticReport, error) {
	s, err := a.Packages(ctx, "installed", "", "")
	engine := a.diagnosticEngine()
	r, e := engine.Diagnose(ctx, name, s.Packages)
	r.Issues = append(r.Issues, s.Issues...)
	if err != nil {
		r.Issues = append(r.Issues, domain.Issue{Message: "Inventory unavailable; PATH-only diagnosis: " + err.Error()})
	}
	return r, e
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9@._+:/-]*$`)

func Validate(req domain.ActionRequest) error {
	if !backend.Known(req.Manager) {
		return fmt.Errorf("unsupported manager %q", req.Manager)
	}
	if !identifier.MatchString(req.Package) {
		return fmt.Errorf("invalid package ID %q; select an exact package ID, not a shell expression", req.Package)
	}
	if strings.HasPrefix(strings.ToLower(req.Package), "pkg:") {
		return fmt.Errorf("use the native package ID with --manager, not a pURL that can override routing")
	}
	for _, part := range strings.Split(req.Package, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid package namespace")
		}
	}
	if (req.Manager == "uvx" || req.Manager == "pipx") && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(req.Package) {
		return fmt.Errorf("use an exact Python project name without a URL or version specifier")
	}
	if req.Manager == "npm" && !regexp.MustCompile(`^(@[A-Za-z0-9._-]+/)?[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(req.Package) {
		return fmt.Errorf("use an exact npm package ID, optionally @scope/name, without a version suffix")
	}
	if req.Version != "" && !identifier.MatchString(req.Version) {
		return fmt.Errorf("invalid version %q", req.Version)
	}
	for _, op := range []string{"install", "upgrade", "remove", "activate"} {
		if req.Operation == op {
			return nil
		}
	}
	return fmt.Errorf("unknown operation %q", req.Operation)
}
func (a *App) Plan(ctx context.Context, req domain.ActionRequest) (domain.ActionPlan, error) {
	req.Manager = backend.NormalizeManager(req.Manager)
	p := domain.ActionPlan{Kind: "package", Request: req, Title: req.Operation + " " + req.Manager + " / " + req.Package}
	if err := Validate(req); err != nil {
		return p, err
	}
	if sameID("uvx", req.Package, "meta-package-manager") && (req.Operation == "remove" || req.Operation == "upgrade") {
		return p, fmt.Errorf("mpm is the active backend; change its version through setup, or switch backend before removing it")
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return p, err
	}
	var selected *domain.Manager
	for _, m := range managers {
		if m.ID == req.Manager {
			copy := m
			selected = &copy
			break
		}
	}
	if selected == nil || !selected.Available {
		return p, fmt.Errorf("%s is unavailable; inspect lazypkg managers or setup", req.Manager)
	}
	if !selected.Supports(req.Operation) {
		return p, fmt.Errorf("%s does not support %s", req.Manager, req.Operation)
	}
	if req.Operation == "install" && req.Manager != "mise" {
		inventory, e := a.packages(ctx, "installed", req.Package, "", false)
		if e != nil {
			p.Warnings = append(p.Warnings, "Existing installations could not be checked: "+e.Error())
		}
		for _, item := range inventory.Packages {
			if !sameID(req.Manager, item.ID, req.Package) {
				continue
			}
			if item.Manager == req.Manager {
				return p, fmt.Errorf("%s is already listed by %s; use upgrade", req.Package, req.Manager)
			}
			p.Warnings = append(p.Warnings, "A matching package ID is already listed by "+item.Manager+"; this may be another copy, but ID equality alone does not prove identity.")
		}
		if len(inventory.Issues) > 0 {
			p.Warnings = append(p.Warnings, "Some managers could not be inventoried; absence of duplicate warnings does not establish that none exist.")
		}
	}
	if req.Operation != "install" {
		s, e := a.packages(ctx, "installed", "", req.Manager, false)
		if e != nil {
			return p, e
		}
		if len(s.Issues) > 0 {
			for _, issue := range s.Issues {
				p.Warnings = append(p.Warnings, issue.Message)
			}
		}
		found := false
		for _, v := range s.Packages {
			if sameID(req.Manager, v.ID, req.Package) && (req.Manager != "mise" || req.Operation == "upgrade" || v.Version == req.Version) {
				found = true
				if req.Manager == "mise" && req.Operation == "remove" && (v.Active || v.Global) {
					p.Warnings = append(p.Warnings, "This version is selected by a known configuration. Removing it leaves that reference missing; configuration is not edited.")
				}
			}
		}
		if !found {
			return p, fmt.Errorf("selected installation no longer exists; refresh inventory")
		}
	}
	if req.Manager == "mise" {
		mi, e := a.mise()
		if e != nil {
			return p, e
		}
		if req.Operation == "remove" || req.Operation == "activate" {
			if req.Version == "" {
				return p, fmt.Errorf("mise %s requires an exact --version", req.Operation)
			}
		} else {
			v, e := mi.Latest(ctx, req.Package, req.Version)
			if e != nil {
				return p, e
			}
			req.Version = v
			p.Request = req
		}
		spec := req.Package + "@" + req.Version
		var args []string
		switch req.Operation {
		case "install", "upgrade":
			args = []string{"install", spec}
			p.Warnings = append(p.Warnings, "Downloads this version only. Existing versions and project configuration are retained; use activate to select it globally.")
		case "remove":
			args = []string{"uninstall", spec}
		case "activate":
			args = []string{"use", "--global", "--pin", spec}
			p.Warnings = append(p.Warnings, "Writes mise global configuration. Project settings may override it; the parent shell updates at its next prompt.")
		}
		c := mi.Command(args...)
		p.Steps = []domain.Step{{ID: "package", Description: p.Title, Command: c}}
		p.Preview = process.Display(c)
		return p, nil
	}
	if req.Operation == "activate" {
		return p, fmt.Errorf("global activation is only available for mise")
	}
	if req.Version != "" {
		return p, fmt.Errorf("--version is supported for mise operations only")
	}
	if req.Manager == "scoop" && req.Operation == "remove" {
		c, e := scoopRemove(selected.Path, req.Package)
		if e != nil {
			return p, e
		}
		p.Steps = []domain.Step{{ID: "package", Description: p.Title, Command: c}}
		p.Preview = process.Display(c)
		p.Warnings = append(p.Warnings, "Targets the current-user Scoop installation; global installations are not removed. Persisted application data is retained (no --purge).")
		return p, nil
	}
	mpm, e := a.provider(ctx)
	if e != nil {
		return p, e
	}
	p.Preview, e = mpm.Preview(ctx, req)
	if e != nil {
		return p, e
	}
	p.Steps = []domain.Step{{ID: "package", Description: p.Title, Command: domain.Command{Path: mpm.Path, Args: []string{"--" + req.Manager, req.Operation, "--", backend.Specifier(req.Manager, req.Package)}}}}
	if req.Operation == "remove" {
		p.Warnings = append(p.Warnings, "The selected manager may remove dependent files or run its uninstall scripts; review its native prompts.")
	}
	return p, nil
}
func scoopRemove(path, pkg string) (domain.Command, error) {
	if runtime.GOOS != "windows" {
		return domain.Command{}, fmt.Errorf("Scoop requires Windows")
	}
	script := strings.TrimSuffix(path, filepath.Ext(path)) + ".ps1"
	if _, err := os.Stat(script); err != nil {
		return domain.Command{}, fmt.Errorf("Scoop PowerShell entrypoint not found: %s", script)
	}
	return domain.Command{Path: "powershell.exe", Args: []string{"-NoProfile", "-File", script, "uninstall", pkg}}, nil
}
func (a *App) Execute(ctx context.Context, p domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	if !a.writeMu.TryLock() {
		return domain.ActionResult{}, fmt.Errorf("another operation is already running")
	}
	defer a.writeMu.Unlock()
	if p.Kind == "setup" {
		r, e := a.Bootstrap.Execute(ctx, p, in, out, errout)
		a.mu.Lock()
		a.mpm = nil
		a.mu.Unlock()
		return r, e
	}
	if p.Kind != "package" {
		return domain.ActionResult{}, fmt.Errorf("unknown plan kind %q", p.Kind)
	}
	req := p.Request
	if err := Validate(req); err != nil {
		return domain.ActionResult{}, err
	}
	mpm, err := a.provider(ctx)
	if err != nil {
		return domain.ActionResult{}, err
	}
	if err = mpm.Recheck(ctx); err != nil {
		return domain.ActionResult{}, err
	}
	fresh, err := a.Plan(ctx, req)
	if err != nil {
		return domain.ActionResult{}, err
	}
	if fresh.Preview != p.Preview || !reflect.DeepEqual(fresh.Request, p.Request) || !reflect.DeepEqual(fresh.Warnings, p.Warnings) {
		return domain.ActionResult{}, fmt.Errorf("operation or its known effects changed; review a new plan")
	}
	var c domain.Command
	done := func() {}
	if req.Manager == "mise" || req.Manager == "scoop" && req.Operation == "remove" {

		c = fresh.Steps[0].Command
	} else {
		mpm, err := a.provider(ctx)
		if err != nil {
			return domain.ActionResult{}, err
		}
		if err = mpm.Check(ctx); err != nil {
			return domain.ActionResult{}, err
		}
		c, done, err = mpm.Mutation(req)
		if err != nil {
			return domain.ActionResult{}, err
		}
	}
	defer done()
	fmt.Fprintln(out, p.Preview)
	err = a.Runner.Run(ctx, c, in, out, errout)
	r := domain.ActionResult{Steps: []domain.StepResult{{ID: "package", Status: "failed"}}, Message: "Operation failed; refresh inventory to inspect any partial changes."}
	if err != nil {
		return r, err
	}
	s, e := a.packages(ctx, "installed", "", req.Manager, false)
	if e != nil || len(s.Issues) > 0 {
		r.Steps[0].Status = "unverified"
		r.Message = "Command exited successfully, but inventory verification is incomplete. Refresh before repeating the operation."
		if e == nil {
			e = errors.New(r.Message)
		}
		return r, e
	}
	found := false
	for _, v := range s.Packages {
		if !sameID(req.Manager, v.ID, req.Package) {
			continue
		}
		if req.Manager == "mise" && v.Version != req.Version {
			continue
		}
		if req.Operation == "activate" && !v.Global {
			continue
		}
		found = true
	}
	if req.Operation == "remove" {
		found = !found
	}
	if !found {
		r.Steps[0].Status = "unverified"
		r.Message = "Command completed but the requested installation state was not observed. Refresh inventory."
		return r, errors.New(r.Message)
	}
	if req.Operation == "upgrade" && req.Manager != "mise" {
		updates, e := a.packages(ctx, "outdated", "", req.Manager, false)
		if e != nil || len(updates.Issues) != 0 {
			r.Steps[0].Status = "unverified"
			r.Message = "Update command completed; current update status could not be verified."
			return r, errors.New(r.Message)
		}
		for _, item := range updates.Packages {
			if sameID(req.Manager, item.ID, req.Package) {
				r.Steps[0].Status = "unverified"
				r.Message = "The package is still reported as outdated; inspect native output before retrying."
				return r, errors.New(r.Message)
			}
		}
	}
	r.Steps[0].Status = "success"
	r.Message = "Operation completed and installation state verified."
	return r, nil
}
func (a *App) SetupOptions(ctx context.Context) ([]domain.SetupOption, error) {
	return a.Bootstrap.Options(ctx)
}
func (a *App) PlanSetup(ctx context.Context, ids []string) (domain.ActionPlan, error) {
	return a.Bootstrap.Plan(ctx, ids)
}

func sameID(manager, a, b string) bool {
	if manager == "uvx" || manager == "pipx" {
		norm := regexp.MustCompile(`[._-]+`)
		return norm.ReplaceAllString(strings.ToLower(a), "-") == norm.ReplaceAllString(strings.ToLower(b), "-")
	}
	if manager == "winget" || manager == "scoop" || manager == "choco" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

type environmentRunner struct {
	process.Runner
	env map[string]string
}

func (r environmentRunner) command(c domain.Command) domain.Command {
	env := make(map[string]string, len(r.env)+len(c.Env))
	for k, v := range r.env {
		env[k] = v
	}
	for k, v := range c.Env {
		env[k] = v
	}
	c.Env = env
	return c
}
func (r environmentRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	return r.Runner.Output(ctx, r.command(c))
}
func (r environmentRunner) Run(ctx context.Context, c domain.Command, in io.Reader, out, errout io.Writer) error {
	return r.Runner.Run(ctx, r.command(c), in, out, errout)
}
func (a *App) diagnosticEngine() *diagnostics.Engine {
	env := a.Bootstrap.ChildEnv()
	e := diagnostics.New(environmentRunner{a.Runner, env})
	if p, ok := env["PATH"]; ok {
		e.Path = p
	}
	return e
}
