package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func (e *Engine) Plan(ctx context.Context, manager domain.Manager) (domain.ActionPlan, error) {
	results, err := e.Check(ctx, []domain.Manager{manager}, false)
	if err != nil {
		return domain.ActionPlan{}, err
	}
	return e.plan(results[0]), nil
}
func (e *Engine) plan(h domain.ManagerHealth) domain.ActionPlan {
	if hostedComponent(h) {
		h.ApplySupported = false
		if h.Recommendation == "" {
			h.Recommendation = "Inspect the hosted component separately from its launcher; no verified component update recipe is available."
		}
	}
	h.Fingerprint = hash([]string{h.Fingerprint, h.Strategy, h.CandidateVersion})
	p := domain.ActionPlan{Kind: "manager", Title: "Update selected " + h.Manager, Request: domain.ActionRequest{Operation: "manager-update", Manager: h.Manager, Version: h.CandidateVersion}, ManagerUpdate: &h}
	if !h.ApplySupported {
		p.Steps = []domain.Step{{ID: "manager-guide", Description: h.Recommendation, GuideURL: h.GuideURL}}
		p.Preview = h.Recommendation
		if h.GuideURL != "" {
			p.Preview += "\n" + h.GuideURL
		}
		return p
	}
	var c domain.Command
	switch h.Strategy {
	case "npm-mise":
		p.Steps = e.npmCommands(h)
		p.Warnings = []string{"This adds a separately managed npm and writes the named global mise configuration. Node versions, bundled npm wrappers and Homebrew alternatives are retained.", "Compatibility was checked against the observed current and global Node contexts; other projects may select different runtimes or override npm.", "The invoking shell is unchanged. Verified new npm paths can be used by subsequent lazypkg operations; shell activation occurs at a later prompt."}
	case "uv-self":
		c = e.command(h.Path, "self", "update", h.CandidateVersion)
		c.Env["UV_NO_MODIFY_PATH"] = "1"
	case "mise-self":
		c = e.command(h.Path, "self-update", "--no-plugins", h.CandidateVersion)
	case "brew-update":
		c = e.command(h.Path, "update")
		p.Warnings = []string{"Homebrew core and formula/cask metadata are refreshed; installed packages are not upgraded by this command."}
	case "brew-package":
		c = e.command(h.OwnerPath, "upgrade", "--formula", h.OwnerPackage)
		p.Warnings = []string{"The selected formula's native dependency changes remain possible; automatic cleanup and automatic metadata refresh are disabled."}
	case "scoop-update":
		ps := e.lookup("pwsh")
		if ps == "" {
			ps = e.lookup("powershell")
		}
		if ps != "" {
			c = e.command(ps, "-NoProfile", "-File", h.OwnerPath, "update")
			p.Warnings = []string{"Scoop and its bucket metadata are refreshed; this command does not upgrade all installed apps."}
		}
	}
	if c.Path != "" {
		p.Steps = []domain.Step{{ID: "manager-update", Description: h.Recommendation, Command: c}}
	}
	var preview strings.Builder
	fmt.Fprintf(&preview, "Selected: %s (%s)\nOwner: %s %s\n", h.Path, h.Version, h.Owner, h.OwnerPackage)
	if h.RuntimePath != "" {
		fmt.Fprintf(&preview, "Runtime: %s (%s)\n", h.RuntimePath, h.RuntimeVersion)
	}
	if h.Prefix != "" {
		fmt.Fprintf(&preview, "Global prefix: %s\n", h.Prefix)
	}
	for _, s := range p.Steps {
		fmt.Fprintln(&preview, s.Description)
		if s.Command.Path != "" {
			fmt.Fprintln(&preview, process.Display(s.Command))
		}
	}
	p.Preview = strings.TrimSpace(preview.String())
	return p
}

func (e *Engine) Execute(ctx context.Context, plan domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	r := domain.ActionResult{Message: "No manager changes were made."}
	if plan.Kind != "manager" || plan.ManagerUpdate == nil {
		return r, errors.New("not a manager maintenance plan")
	}
	h := *plan.ManagerUpdate
	if !h.ApplySupported || h.Fingerprint == "" || hostedComponent(h) {
		return r, errors.New("this manager has guidance only; no update recipe was approved")
	}
	m := domain.Manager{ID: h.Manager, Path: h.Path, Version: h.Version, Requirement: h.Requirement, Available: h.Compatible, Reason: h.Reason, ReasonCode: h.ReasonCode, ComponentKind: h.ComponentKind, VersionSubject: h.VersionSubject, Launcher: h.Launcher}
	if selected := e.lookup(executableName(h.Manager)); selected != "" && canonical(selected) != canonical(h.Path) {
		return r, errors.New("the selected manager on PATH changed; review a new update plan")
	}
	observed := e.observe(ctx, m)
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if hash([]string{observed.Health.Fingerprint, h.Strategy, h.CandidateVersion}) != h.Fingerprint || observed.Health.Strategy != h.Strategy {
		return r, errors.New("selected manager, runtime, prefix, owner, configuration or reviewed target changed; review a new update plan")
	}
	if h.Strategy == "npm-mise" {
		if err := e.npmVersionCheck(ctx, observed, h.CandidateVersion); err != nil {
			return r, err
		}
	} else if h.Strategy != "brew-update" && h.Strategy != "scoop-update" {
		text, err := e.managerVersion(ctx, h)
		if err != nil {
			return r, err
		}
		actual, ok := version(text)
		expected, known := version(h.Version)
		if !ok || !known || actual.compare(expected) != 0 {
			return r, errors.New("the selected manager version changed; review a new update plan")
		}
	}
	if h.Strategy == "brew-package" && observed.Health.CandidateVersion != h.CandidateVersion {
		return r, errors.New("the owning formula's candidate changed; review a new update plan")
	}
	if h.Strategy != "brew-update" && h.Strategy != "scoop-update" {
		candidate, ok := version(h.CandidateVersion)
		current, currentOK := version(h.Version)
		if !ok || !currentOK || candidate.compare(current) <= 0 {
			return r, errors.New("reviewed target is not a newer stable version")
		}
	}
	// Rebuild commands from verified facts, never execute caller-supplied steps.
	fresh := observed.Health
	fresh.CandidateVersion = h.CandidateVersion
	fresh.ApplySupported = true
	approved := e.plan(fresh)
	if len(approved.Steps) == 0 {
		return r, errors.New("the selected update recipe is unavailable")
	}
	originalWrapper := fileDigest(observed.NPMEntrypoint)
	originalNode := identity(h.RuntimePath)
	for _, step := range approved.Steps {
		if step.Command.Path == "" {
			return r, errors.New("update recipe contains a guidance-only step")
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		fmt.Fprintln(out, process.Display(step.Command))
		err := e.Runner.Run(ctx, step.Command, in, out, errout)
		state := "success"
		if err != nil {
			state = "failed"
		}
		r.Steps = append(r.Steps, domain.StepResult{ID: step.ID, Status: state})
		if err != nil {
			r.Message = "Manager update stopped. Completed installations or configuration changes are retained; inspect the result before retrying."
			return r, err
		}
		if h.Strategy == "npm-mise" && step.ID == "manager-install" {
			if _, _, err := e.installedNPM(ctx, fresh); err != nil {
				r.Steps[len(r.Steps)-1].Status = "unverified"
				r.Message = "npm was installed but could not be verified; global configuration was not changed."
				return r, err
			}
			if fileDigest(observed.NPMEntrypoint) != originalWrapper || identity(h.RuntimePath) != originalNode {
				r.Steps[len(r.Steps)-1].Status = "unverified"
				r.Message = "The original npm entrypoint changed unexpectedly; global configuration was not changed."
				return r, errors.New(r.Message)
			}
			oldVersion, err := e.query(ctx, h.RuntimePath, observed.NPMCLI, "--version")
			old, ok := version(oldVersion)
			if err != nil || !ok || old.String() != h.Version {
				r.Steps[len(r.Steps)-1].Status = "unverified"
				r.Message = "The original npm distribution changed unexpectedly; global configuration was not changed."
				return r, errors.New(r.Message)
			}
		}
	}
	if h.Strategy == "npm-mise" {
		return e.verifyNPM(ctx, observed, h, r)
	}
	text, err := e.managerVersion(ctx, h)
	if err != nil {
		r.Message = "Update command finished, but the selected manager could not be verified."
		r.Steps[len(r.Steps)-1].Status = "unverified"
		return r, err
	}
	actual, ok := version(text)
	if !ok && h.Strategy == "brew-update" {
		fields := strings.Fields(text)
		if len(fields) >= 2 && fields[0] == "Homebrew" {
			base, _, _ := strings.Cut(fields[1], "-")
			actual, ok = version(base)
		}
	}
	if !ok {
		r.Message = "The update command exited successfully, but the selected manager's version probe was not recognizable."
		r.Steps[len(r.Steps)-1].Status = "unverified"
		return r, errors.New(r.Message)
	}
	if h.Strategy != "brew-update" && h.Strategy != "scoop-update" && (!ok || actual.String() != h.CandidateVersion) {
		r.Message = "Update command finished, but the selected instance does not report the reviewed version."
		r.Steps[len(r.Steps)-1].Status = "unverified"
		return r, errors.New(r.Message)
	}
	if ok && !minimum(actual, h.Requirement) {
		r.Message = "The selected manager updated but still does not satisfy the backend version requirement."
		return r, errors.New(r.Message)
	}
	r.Message = "The selected manager was updated and its version verified. Later PATH alternatives were not selected."
	return r, nil
}

func (e *Engine) installedNPM(ctx context.Context, h domain.ManagerHealth) (string, []string, error) {
	spec := "aqua:npm/cli@" + h.CandidateVersion
	root, err := e.query(ctx, h.OwnerPath, "where", spec)
	if err != nil {
		return "", nil, err
	}
	root = strings.TrimSpace(root)
	if !filepath.IsAbs(root) {
		return "", nil, errors.New("mise returned a non-absolute npm installation path")
	}
	var cli string
	for _, candidate := range []string{filepath.Join(root, "package", "bin", "npm-cli.js"), filepath.Join(root, "bin", "npm-cli.js"), filepath.Join(root, "lib", "node_modules", "npm", "bin", "npm-cli.js"), filepath.Join(root, "node_modules", "npm", "bin", "npm-cli.js")} {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			cli = candidate
			break
		}
	}
	if cli == "" {
		return "", nil, errors.New("installed npm CLI could not be found within the mise installation")
	}
	if !within(canonical(cli), canonical(root)) {
		return "", nil, errors.New("npm CLI resolves outside its verified mise installation")
	}
	text, err := e.query(ctx, h.RuntimePath, cli, "--version")
	if err != nil {
		return "", nil, err
	}
	if v, ok := version(text); !ok || v.String() != h.CandidateVersion {
		return "", nil, errors.New("installed independent npm reports a different version")
	}
	prefix, err := e.query(ctx, h.RuntimePath, cli, "prefix", "--global")
	if err != nil {
		return "", nil, err
	}
	if canonical(strings.TrimSpace(prefix)) != h.Prefix {
		return "", nil, errors.New("independent npm would change the selected Node global package prefix")
	}
	paths, err := e.query(ctx, h.OwnerPath, "bin-paths", spec)
	if err != nil {
		return "", nil, err
	}
	var dirs []string
	for _, dir := range strings.Split(strings.TrimSpace(paths), "\n") {
		dir = strings.TrimSpace(dir)
		if !filepath.IsAbs(dir) || !within(canonical(dir), canonical(root)) {
			return "", nil, errors.New("mise reported a bin directory outside the verified npm installation")
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return "", nil, errors.New("mise returned no npm executable directories")
	}
	return root, dirs, nil
}

func (e *Engine) managerVersion(ctx context.Context, h domain.ManagerHealth) (string, error) {
	if h.Manager == "go" {
		return e.query(ctx, h.Path, "version")
	}
	if h.Strategy == "scoop-update" {
		ps := e.lookup("pwsh")
		if ps == "" {
			ps = e.lookup("powershell")
		}
		if ps == "" {
			return "", errors.New("PowerShell is unavailable for Scoop verification")
		}
		return e.query(ctx, ps, "-NoProfile", "-File", h.OwnerPath, "--version")
	}
	return e.query(ctx, h.Path, "--version")
}
func (e *Engine) verifyNPM(ctx context.Context, before observation, h domain.ManagerHealth, r domain.ActionResult) (domain.ActionResult, error) {
	root, dirs, err := e.installedNPM(ctx, h)
	if err != nil {
		r.Message = "Global npm activation completed, but installation verification is incomplete."
		return r, err
	}
	text, err := e.global(ctx, h.OwnerPath, "ls", "--global", "--json")
	if err != nil {
		return r, err
	}
	global, err := parseTools(text)
	if err != nil {
		return r, err
	}
	found := false
	for id, entries := range global {
		if id != "npm" && id != "aqua:npm/cli" {
			continue
		}
		for _, v := range entries {
			found = found || v.Version == h.CandidateVersion && canonical(v.Path) == canonical(root)
		}
	}
	if !found {
		r.Message = "The independent npm installation exists, but the requested global pin was not observed."
		return r, errors.New(r.Message)
	}
	if len(global["node"]) == 0 || global["node"][0].Version != before.GlobalNode {
		return r, errors.New("the global Node selection changed unexpectedly; inspect mise configuration")
	}
	nodeVersion, err := e.query(ctx, h.RuntimePath, "--version")
	if err != nil {
		return r, err
	}
	node, ok := version(nodeVersion)
	if !ok || node.String() != h.RuntimeVersion {
		return r, errors.New("the selected Node runtime changed unexpectedly")
	}
	selected, err := e.query(ctx, h.OwnerPath, "which", "npm")
	if err != nil {
		return r, err
	}
	selected = strings.TrimSpace(selected)
	if !filepath.IsAbs(selected) {
		return r, errors.New("mise returned a non-absolute effective npm path")
	}
	if st, err := os.Stat(selected); err != nil || st.IsDir() {
		return r, errors.New("the effective npm entrypoint could not be verified")
	}
	if !within(canonical(strings.TrimSpace(selected)), canonical(root)) {
		r.Message = "Independent npm " + h.CandidateVersion + " is installed and pinned globally. The current mise context overrides that selection; its PATH was retained. The invoking shell is unchanged."
		return r, nil
	}
	e.mu.Lock()
	for _, dir := range dirs {
		seen := false
		for _, old := range e.additions {
			seen = seen || old == dir
		}
		if !seen {
			e.additions = append(e.additions, dir)
		}
	}
	e.mu.Unlock()
	r.Message = "Independent npm " + h.CandidateVersion + " is installed and pinned globally. Node, bundled npm wrappers and the global package prefix were retained. Subsequent lazypkg operations use the new npm; the invoking shell awaits its next mise activation."
	return r, nil
}
