package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func (g *GHExtensions) selected(ctx context.Context, id string) (domain.GHExtension, error) {
	xs, _, err := g.Inspect(ctx)
	if err != nil {
		return domain.GHExtension{}, err
	}
	for _, x := range xs {
		if x.ID == id {
			return x, nil
		}
	}
	return domain.GHExtension{}, errors.New("the selected extension repository is no longer registered; refresh inventory")
}
func ghWritable(x domain.GHExtension, operation string) bool {
	return (x.Kind == "binary" || x.Kind == "git") && (!x.Pinned || operation == "remove") && x.BlockedReason == "" && x.Status != "unknown" && x.Status != "local" && (x.Status != "pinned" || operation == "remove") && ghRepoID(x.ID) && x.FullVersion != ""
}
func ghPlanFingerprint(p domain.GHExtensionPlan) string {
	return ghDigest([]string{"gh-plan-v1", p.Operation, p.Before.Fingerprint, p.Before.Latest, p.Before.Status})
}

// Plan handles verified remote extension upgrades/removals. Search and install
// retain mpm's stable gh-ext adapter routing. Local/dirty/unknown targets remain
// guidance-only; a pinned remote permits explicit removal, but never upgrade.
func (g *GHExtensions) Plan(ctx context.Context, req domain.ActionRequest) (domain.ActionPlan, error) {
	p := domain.ActionPlan{Kind: "package", Request: req, Title: req.Operation + " gh extension " + req.Package}
	if req.Manager != "gh-ext" || req.Version != "" || (req.Operation != "upgrade" && req.Operation != "remove") {
		return p, errors.New("native gh extension plan requires upgrade/remove and an exact installed repository ID")
	}
	x, err := g.selected(ctx, req.Package)
	if err != nil {
		return p, err
	}
	if !ghWritable(x, req.Operation) {
		return p, fmt.Errorf("%s extension is not eligible for this operation: %s %s", x.Status, x.Reason, x.BlockedReason)
	}
	if req.Operation == "upgrade" {
		x, err = g.Check(ctx, x)
		if err != nil {
			return p, err
		}
		if x.Status != "available" && x.Status != "current" {
			return p, fmt.Errorf("extension update is %s: %s", x.Status, x.Reason)
		}
	}
	binding := domain.GHExtensionPlan{Operation: req.Operation, Before: x}
	binding.Fingerprint = ghPlanFingerprint(binding)
	p.GHExtension = &binding
	c := g.command(g.Path, "extension", req.Operation, x.ID)
	p.Preview = fmt.Sprintf("Selected: %s (%s)\nHost: %s\nRegistration: %s\n", x.ID, x.Version, x.Host, x.Path)
	if x.Pinned && req.Operation == "remove" {
		p.Preview += "Pinned registration: this explicit removal also removes its pin.\n"
	}
	if x.Status == "current" && req.Operation == "upgrade" {
		p.Preview += "Already current; no update command is needed."
		return p, nil
	}
	p.Steps = []domain.Step{{ID: "gh-extension", Description: p.Title, Command: c}}
	p.Preview += process.Display(c)
	if req.Operation == "upgrade" {
		p.Preview += "\nObserved candidate: " + x.Latest
		p.Warnings = []string{"The native command resolves the latest release or branch at execution time; the observed candidate is not a version pin. Pinned and local extensions are excluded, and --force/--all are never used."}
	}
	return p, nil
}

// Execute reconstructs the command after validating the observed registration;
// caller-supplied Steps are never executed.
func (g *GHExtensions) Execute(ctx context.Context, p domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	r := domain.ActionResult{Message: "No extension changes were made."}
	if p.Kind != "package" || p.GHExtension == nil {
		return r, errors.New("not a bound gh extension plan")
	}
	b := *p.GHExtension
	if p.Request.Manager != "gh-ext" || p.Request.Package != b.Before.ID || p.Request.Operation != b.Operation || p.Request.Version != "" || b.Fingerprint == "" || b.Fingerprint != ghPlanFingerprint(b) || !ghWritable(b.Before, b.Operation) {
		return r, errors.New("extension plan binding is invalid")
	}
	if b.Operation != "upgrade" && b.Operation != "remove" {
		return r, errors.New("unsupported extension operation")
	}
	x, err := g.selected(ctx, b.Before.ID)
	if err != nil {
		return r, err
	}
	if x.Fingerprint != b.Before.Fingerprint || !ghWritable(x, b.Operation) {
		return r, errors.New("extension repository, root, launcher, version or pin changed; review a new plan")
	}
	if b.Operation == "upgrade" {
		x, err = g.Check(ctx, x)
		if err != nil {
			return r, err
		}
		if x.Status == "pinned" || x.Status == "local" {
			r.Steps = []domain.StepResult{{ID: "gh-extension", Status: "skipped", Message: x.Reason}}
			r.Message = x.Reason
			return r, nil
		}
		if x.Status != "current" && x.Status != "available" {
			return r, errors.New("extension update status is not verified")
		}
		if x.Status == "current" {
			r.Steps = []domain.StepResult{{ID: "gh-extension", Status: "current", Message: "Already current"}}
			r.Message = "Extension is already current; no update command was needed."
			return r, nil
		}
		if x.Latest != b.Before.Latest || x.Status != b.Before.Status {
			return r, errors.New("extension candidate changed; review a new plan")
		}
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	c := g.command(g.Path, "extension", b.Operation, x.ID)
	if out == nil {
		out = io.Discard
	}
	if errout == nil {
		errout = io.Discard
	}
	fmt.Fprintln(out, process.Display(c))
	if err := g.runner().Run(ctx, c, in, out, errout); err != nil {
		r.Steps = []domain.StepResult{{ID: "gh-extension", Status: "failed"}}
		r.Message = "Native extension operation failed; refresh before retrying."
		return r, errors.Join(errors.New(r.Message), ctx.Err())
	}
	return g.Verify(ctx, b)
}

// Verify distinguishes an observed update from a successful native no-op. It
// never treats an unsupported/failed update check as proof of being current.
func (g *GHExtensions) Verify(ctx context.Context, b domain.GHExtensionPlan) (domain.ActionResult, error) {
	r := domain.ActionResult{Steps: []domain.StepResult{{ID: "gh-extension", Status: "unverified"}}, Message: "Extension operation completed, but its result could not be verified."}
	xs, _, err := g.Inspect(ctx)
	if err != nil {
		return r, errors.Join(errors.New(r.Message), ctx.Err())
	}
	var after *domain.GHExtension
	for _, x := range xs {
		if x.Name == b.Before.Name {
			copy := x
			after = &copy
			break
		}
	}
	if b.Operation == "remove" {
		if after != nil {
			return r, errors.New(r.Message)
		}
		r.Steps[0].Status = "success"
		r.Message = "Extension registration was removed and absence verified."
		return r, nil
	}
	if after == nil || after.ID != b.Before.ID || after.Host != b.Before.Host || after.Root != b.Before.Root || after.Launcher != b.Before.Launcher {
		return r, errors.New(r.Message)
	}
	if after.BlockedReason != "" {
		return r, errors.New(r.Message)
	}
	if after.Status == "pinned" || after.Status == "local" {
		r.Steps[0].Status = "skipped"
		r.Message = "Extension is now pinned or local; no verified update is claimed."
		return r, nil
	}
	checked, err := g.Check(ctx, *after)
	if err != nil || checked.Status != "current" {
		return r, errors.New(r.Message)
	}
	if checked.FullVersion == b.Before.FullVersion {
		r.Steps[0].Status = "current"
		r.Message = "Extension remained at its installed version and is verified current."
	} else {
		r.Steps[0].Status = "success"
		r.Steps[0].Message = "updated"
		r.Message = "Extension was updated and is verified current."
	}
	return r, nil
}

// ValidateGHExtensionID keeps remote install/search selections separate from
// local paths, URLs and short names whose owner cannot be verified.
func ValidateGHExtensionID(id string) error {
	if !ghRepoID(id) || strings.ContainsAny(id, "\x00\r\n\x1b") {
		return errors.New("use an exact owner/gh-repository extension ID")
	}
	return nil
}
