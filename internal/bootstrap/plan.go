package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

func (e *Engine) Plan(ctx context.Context, ids []string) (domain.ActionPlan, error) {
	plan := domain.ActionPlan{Kind: "setup", Title: "Set up package managers", SetupIDs: append([]string(nil), ids...)}
	if len(ids) == 0 {
		return plan, errors.New("select at least one setup item")
	}
	opts, err := e.Options(ctx)
	if err != nil {
		return plan, err
	}
	options := map[string]domain.SetupOption{}
	for _, o := range opts {
		options[o.ID] = o
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if _, ok := options[id]; !ok {
			return plan, fmt.Errorf("unknown setup item %q on %s", id, e.GOOS)
		}
		selected[id] = true
	}
	if selected["mpm"] && selected["mpm-standalone"] {
		return plan, errors.New("choose one mpm installation method: uv tool or standalone")
	}
	added := map[string]bool{}
	var add func(string) error
	add = func(id string) error {
		if added[id] {
			return nil
		}
		added[id] = true
		o := options[id]
		if o.Installed {
			plan.Steps = append(plan.Steps, domain.Step{ID: id, Description: o.Name + " is already installed; skip"})
			return nil
		}
		step := domain.Step{ID: id, Description: "Install " + o.Name}
		if o.GuideURL != "" && id != "winget" && id != "choco" {
			step.GuideURL = o.GuideURL
			step.Description = o.Description
			plan.Steps = append(plan.Steps, step)
			return nil
		}
		guide := func(url string, reason error) {
			step.Command = domain.Command{}
			step.URL = ""
			step.GuideURL = url
			step.Description = reason.Error() + ". Follow official setup guidance."
		}
		dependency := func(dep string) error {
			if err := add(dep); err != nil {
				return err
			}
			step.DependsOn = append(step.DependsOn, dep)
			return nil
		}
		switch id {
		case "winget", "choco":
			guide(o.GuideURL, errors.New(o.Name+" bootstrap requires official installation or repair guidance"))
		case "mpm-standalone":
			asset, err := e.releaseAsset(ctx)
			if err != nil {
				return err
			}
			step.URL = asset.URL
			step.Digest = asset.Digest
			step.Destination = e.standalonePath()
			step.Description = "Download official mpm " + domain.MPMVersion + "; verify pinned SHA-256; install at " + step.Destination
			step.Verify = &domain.Command{Path: step.Destination, Args: []string{"--version"}}
		case "mpm":
			if err := dependency("uv"); err != nil {
				return err
			}
			step.Command = e.command("uv", "tool", "install", "meta-package-manager=="+domain.MPMVersion)
			step.Description = "Install mpm " + domain.MPMVersion + " in your normal uv tool environment (no --force); uv may obtain Python if needed"
			if path, err := e.Lookup("mpm"); err == nil {
				version := "unknown version"
				if res, err := e.output(ctx, domain.Command{Path: path, Args: []string{"--version"}, Env: e.ChildEnv()}); err == nil {
					if v := versionPattern.FindString(res.Stdout); v != "" {
						version = "version " + v
					}
				}
				step.Description += "; existing mpm (" + version + ") detected at " + path + ". This explicitly changes the normal uv tool constraint to " + domain.MPMVersion
			}
			step.Verify = &domain.Command{Path: "mpm", Args: []string{"--version"}}
		case "brew":
			step.URL = "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh"
			step.Command = domain.Command{Path: "/bin/bash", Args: []string{installerToken}}
			step.Description = "Run official Homebrew installer; it may request administrator rights and Command Line Tools"
			step.Verify = &domain.Command{Path: "brew", Args: []string{"--version"}}
		case "scoop":
			ps, err := e.windowsReady(ctx, true)
			if err != nil {
				guide("https://github.com/ScoopInstaller/Install", err)
				break
			}
			step.URL = "https://get.scoop.sh"
			step.Command = domain.Command{Path: ps, Args: []string{"-NoProfile", "-File", installerToken}}
			step.Description = "Install Scoop for the current user and add its shims to User PATH; keep current execution policy"
			step.Verify = &domain.Command{Path: "scoop", Args: []string{"--version"}}
		case "uv", "mise":
			if e.GOOS == "darwin" && (options["brew"].Installed || selected["brew"]) {
				if err := dependency("brew"); err != nil {
					return err
				}
				step.Command = e.command("brew", "install", id)
			} else if e.GOOS == "windows" && id == "mise" {
				if options["scoop"].Installed || selected["scoop"] || !options["winget"].Installed {
					if err := dependency("scoop"); err != nil {
						return err
					}
					step.Command, err = e.scoopCommand("install", "mise")
					if err != nil {
						step.Command = domain.Command{Path: "scoop", Args: []string{"install", "mise"}}
					}
				} else {
					step.Command = e.command("winget", "install", "--id", "jdx.mise", "--exact", "--source", "winget")
				}
			} else if e.GOOS == "windows" {
				ps, err := e.windowsReady(ctx, false)
				if err != nil {
					guide("https://docs.astral.sh/uv/getting-started/installation/", err)
					break
				}
				step.URL = "https://astral.sh/uv/install.ps1"
				step.Command = domain.Command{Path: ps, Args: []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installerToken}}
			} else {
				step.URL = "https://astral.sh/uv/install.sh"
				if id == "mise" {
					step.URL = "https://mise.run"
				}
				step.Command = domain.Command{Path: "/bin/sh", Args: []string{installerToken}}
			}
			step.Verify = &domain.Command{Path: id, Args: []string{"--version"}}
			if id == "uv" && step.URL != "" {
				step.Description = "Run official uv installer in the user executable directory; installer may update User PATH or shell profiles"
			}
			if id == "mise" {
				step.Description = "Install mise; no runtimes or shell activation are added by lazypkg"
			}
		}
		plan.Steps = append(plan.Steps, step)
		return nil
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if err := add(id); err != nil {
			return plan, err
		}
	}
	plan.Warnings = []string{"Only the listed setup steps are applied. Existing healthy installations are skipped.", "Completed external installations are kept if a later step fails or is cancelled.", "New executable paths apply to subsequent lazypkg operations. Other terminals may need a new session; mise shell activation is separate."}
	var preview strings.Builder
	for _, s := range plan.Steps {
		fmt.Fprintf(&preview, "%s: %s\n", s.ID, s.Description)
		if len(s.DependsOn) > 0 {
			fmt.Fprintf(&preview, "  after: %s\n", strings.Join(s.DependsOn, ", "))
		}
		if s.URL != "" {
			fmt.Fprintf(&preview, "  source: %s\n", s.URL)
		}
		if s.Digest != "" {
			fmt.Fprintf(&preview, "  SHA-256: %s\n", s.Digest)
		}
		if s.Command.Path != "" {
			fmt.Fprintf(&preview, "  %s\n", process.Display(s.Command))
		}
		if s.GuideURL != "" {
			fmt.Fprintf(&preview, "  %s\n", s.GuideURL)
		}
	}
	plan.Preview = preview.String()
	return plan, nil
}

func targetName(id string) string {
	if id == "mpm-standalone" {
		return "mpm"
	}
	return id
}

func (e *Engine) resolveAfter(ctx context.Context, id string) (string, error) {
	if id == "mpm-standalone" {
		return e.standalonePath(), nil
	}
	if id == "mpm" {
		res, err := e.output(ctx, e.command("uv", "tool", "dir", "--bin"))
		if err != nil {
			return "", fmt.Errorf("find uv tool executable directory: %w", err)
		}
		dir := strings.TrimSpace(res.Stdout)
		if !filepath.IsAbs(dir) {
			return "", errors.New("uv returned a non-absolute tool executable directory")
		}
		name := "mpm"
		if e.GOOS == "windows" {
			name += ".exe"
		}
		return filepath.Join(dir, name), nil
	}
	return e.Lookup(id)
}
