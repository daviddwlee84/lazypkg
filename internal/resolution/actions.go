package resolution

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func installation(a domain.ConflictAssessment, id string) (domain.ConflictInstallation, bool) {
	for _, i := range a.Installations {
		if i.ID == id {
			return i, true
		}
	}
	return domain.ConflictInstallation{}, false
}

func sameProject(a, b domain.ConflictInstallation) bool {
	if a.Project == "" || a.Project != b.Project {
		return false
	}
	// Repository equality alone would merge unrelated tools in a monorepo.
	return domain.NormalizePackageID("uvx", filepath.Base(a.Package.ID)) == domain.NormalizePackageID("uvx", filepath.Base(b.Package.ID)) && (a.Package.Manager != "npm" || b.Package.Manager != "npm" || a.Package.ID == b.Package.ID)
}

func pathWinner(a domain.ConflictAssessment, except string) string {
	var paths []domain.Executable
	for _, i := range a.Installations {
		if i.ID == except {
			continue
		}
		for _, path := range i.Paths {
			if path.PathIndex >= 0 && path.Problem == "" {
				paths = append(paths, path)
			}
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return paths[i].PathIndex < paths[j].PathIndex })
	if len(paths) > 0 {
		return paths[0].Path
	}
	return ""
}

func (e *Engine) Plan(ctx context.Context, req domain.ResolutionRequest) (domain.ActionPlan, error) {
	p := domain.ActionPlan{Kind: "resolution", Title: "Resolve " + req.Name}
	if e.Fresh == nil {
		return p, fmt.Errorf("fresh conflict assessment is required")
	}
	a, err := e.Fresh(ctx, req.Name)
	if err != nil {
		return p, err
	}
	return e.plan(req, a)
}

func (e *Engine) plan(req domain.ResolutionRequest, a domain.ConflictAssessment) (domain.ActionPlan, error) {
	p := domain.ActionPlan{Kind: "resolution", Title: "Resolve " + req.Name}
	if req.Name == "" || a.Name != req.Name || req.KeepID == "" || req.RemoveID == "" || req.KeepID == req.RemoveID {
		return p, fmt.Errorf("select distinct retained and removed installation IDs")
	}
	keep, ok := installation(a, req.KeepID)
	if !ok {
		return p, fmt.Errorf("retained installation changed; refresh the assessment")
	}
	remove, ok := installation(a, req.RemoveID)
	if !ok {
		return p, fmt.Errorf("removal target changed; refresh the assessment")
	}
	if remove.Status != "ready" || len(remove.Blockers) > 0 {
		return p, fmt.Errorf("removal is not ready: %s", strings.Join(remove.Blockers, "; "))
	}
	if !sameProject(keep, remove) {
		return p, fmt.Errorf("these installations are not verified as the same project; inspect their identities first")
	}
	if keep.Package.Root == "" || remove.Package.Root == "" || within(keep.Package.Root, remove.Package.Root) || within(remove.Package.Root, keep.Package.Root) {
		return p, fmt.Errorf("installation roots overlap or are unknown; no removal is proposed")
	}
	if within(keep.ManagerPath, remove.Package.Root) || within(keep.RuntimePath, remove.Package.Root) {
		return p, fmt.Errorf("the retained installation depends on a manager or runtime inside the removal target")
	}
	for _, path := range keep.RequiredPaths {
		if within(path, remove.Package.Root) {
			return p, fmt.Errorf("the retained installation requires a package inside the removal target: %s", path)
		}
	}
	accessible := false
	for _, path := range keep.Paths {
		if path.Problem == "" && path.PathIndex >= 0 && executable(path.Path, e.GOOS) {
			accessible = true
		}
	}
	if !accessible {
		return p, fmt.Errorf("the retained command is not executable on this PATH; activate it before removing another source")
	}
	command, err := e.removeCommand(remove)
	if err != nil {
		return p, err
	}
	r := domain.ResolutionPlan{Request: req, Keep: keep, Remove: remove, Command: command, ExpectedPath: pathWinner(a, remove.ID)}
	r.Fingerprint = digest([]any{a.Fingerprint, req, keep.Fingerprint, remove.Fingerprint, command, r.ExpectedPath})
	p.Resolution = &r
	p.Title = "Remove " + remove.Package.Manager + " / " + remove.Package.ID + "; retain " + keep.Package.Manager + " / " + keep.Package.ID
	p.Request = domain.ActionRequest{Operation: "remove", Manager: remove.Package.Manager, Package: remove.Package.ID, Version: remove.Package.Version}
	p.Steps = []domain.Step{{ID: "resolution-remove", Description: p.Title, Command: command}}
	p.Preview = process.Display(command)
	p.Warnings = append(p.Warnings, remove.Warnings...)
	p.Warnings = append(p.Warnings, "Retained installation: "+keep.Package.Root, "Removed installation: "+remove.Package.Root, "All removed commands: "+strings.Join(remove.Commands, ", "))
	if !keep.Effective {
		p.Warnings = append(p.Warnings, "After this one removal, the expected PATH entry is "+r.ExpectedPath+". Other sources may still precede the retained source.")
	}
	return p, nil
}

func (e *Engine) Execute(ctx context.Context, p domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	result := domain.ActionResult{}
	if p.Kind != "resolution" || p.Resolution == nil || e.Fresh == nil {
		return result, fmt.Errorf("a reviewed resolution plan and fresh assessment are required")
	}
	fresh, err := e.Plan(ctx, p.Resolution.Request)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(fresh, p) {
		return result, fmt.Errorf("the removal context, dependency result or reviewed plan changed; review a new plan")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	r := fresh.Resolution
	err = e.Runner.Run(ctx, r.Command, in, out, errout)
	if err != nil {
		result.Steps = []domain.StepResult{{ID: "resolution-remove", Status: "failed", Message: err.Error()}}
		result.Message = "Removal did not complete; refresh before retrying."
		return result, err
	}
	result.Steps = []domain.StepResult{{ID: "resolution-remove", Status: "unverified"}}
	a, err := e.Fresh(ctx, r.Request.Name)
	if err != nil {
		result.Message = "The manager returned success, but fresh verification failed."
		return result, err
	}
	if _, exists := installation(a, r.Remove.ID); exists {
		result.Message = "The manager returned success, but the selected installation is still present."
		return result, fmt.Errorf("removal postcheck: selected installation remains")
	}
	if _, err := os.Stat(r.Remove.Package.Root); err == nil || !os.IsNotExist(err) {
		return result, fmt.Errorf("removal postcheck: the selected installation root is still present or cannot be checked")
	}
	keep, exists := installation(a, r.Keep.ID)
	if !exists || keep.Package.Key() != r.Keep.Package.Key() || keep.Project != r.Keep.Project || keep.ArtifactFingerprint != r.Keep.ArtifactFingerprint {
		result.Message = "The retained installation changed; inspect the fresh diagnosis."
		return result, fmt.Errorf("removal postcheck: retained installation was not verified")
	}
	for _, previous := range r.Keep.Paths {
		if previous.PathIndex < 0 || previous.Problem != "" {
			continue
		}
		found := false
		for _, now := range keep.Paths {
			if previous.Path == now.Path && canonical(previous.Target) == canonical(now.Target) && now.Problem == "" && executable(now.Path, e.GOOS) {
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("removal postcheck: retained entrypoint changed: %s", previous.Path)
		}
	}
	if actual := pathWinner(a, ""); actual != r.ExpectedPath {
		return result, fmt.Errorf("removal completed but PATH resolution changed unexpectedly; expected %s, found %s", r.ExpectedPath, actual)
	}
	retainedPath := ""
	index := int(^uint(0) >> 1)
	for _, path := range keep.Paths {
		if path.PathIndex >= 0 && path.PathIndex < index && path.Problem == "" {
			retainedPath = path.Path
			index = path.PathIndex
		}
	}
	result.Message = "Removed " + r.Remove.Package.Manager + " / " + r.Remove.Package.ID + "; retained command remains available at " + retainedPath + "."
	if retainedPath != r.ExpectedPath {
		result.Message += " PATH still selects " + r.ExpectedPath + "; review the remaining sources individually."
	}
	result.Message += " Parent-shell aliases and command caches are not inspected."
	result.Steps[0].Status = "success"
	return result, nil
}
