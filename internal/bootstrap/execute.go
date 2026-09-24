package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (e *Engine) validatePlan(plan domain.ActionPlan) error {
	if plan.Kind != "setup" {
		return errors.New("bootstrap can execute only setup plans")
	}
	seen := map[string]bool{}
	for _, s := range plan.Steps {
		if seen[s.ID] {
			return fmt.Errorf("duplicate setup step %q", s.ID)
		}
		switch s.ID {
		case "mpm", "mpm-standalone", "uv", "mise":
		case "brew":
			if e.GOOS != "darwin" {
				return errors.New("Homebrew bootstrap is only supported on macOS")
			}
		case "scoop", "winget", "choco":
			if e.GOOS != "windows" {
				return errors.New("Windows setup step on a non-Windows host")
			}
		default:
			return fmt.Errorf("unsupported setup step %q", s.ID)
		}
		for _, dep := range s.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("setup dependency %q must precede %q", dep, s.ID)
			}
		}
		seen[s.ID] = true
		if err := e.validateCommand(s); err != nil {
			return err
		}
		if s.URL != "" {
			if s.ID == "mpm-standalone" {
				a, err := e.expectedAsset()
				if err != nil {
					return err
				}
				if s.URL != a.URL || s.Digest != a.Digest || s.Destination != e.standalonePath() || !filepath.IsAbs(e.DataDir) {
					return errors.New("standalone plan does not match the pinned release or data directory")
				}
			} else {
				want := map[string]string{"brew": "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh", "scoop": "https://get.scoop.sh", "mise": "https://mise.run", "uv": "https://astral.sh/uv/install.sh"}[s.ID]
				if s.ID == "uv" && e.GOOS == "windows" {
					want = "https://astral.sh/uv/install.ps1"
				}
				if want == "" || s.URL != want {
					return errors.New("setup plan contains an unrecognized installer source")
				}
			}
		}
	}
	return nil
}

// Plans carry reviewable commands, but are not an arbitrary command runner.
func (e *Engine) validateCommand(s domain.Step) error {
	if s.Command.Path == "" {
		return nil
	}
	if s.Command.Dir != "" || len(s.Command.Unset) > 0 {
		return errors.New("setup plan contains unexpected process overrides")
	}
	for k := range s.Command.Env {
		if k != "PATH" {
			return fmt.Errorf("setup plan contains unexpected environment override %s", k)
		}
	}
	allowed := []domain.Command{}
	if s.URL != "" {
		if e.GOOS == "windows" {
			ps, err := e.powerShell()
			if err != nil {
				return err
			}
			args := []string{"-NoProfile", "-File", installerToken}
			if s.ID == "uv" {
				args = []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installerToken}
			}
			allowed = append(allowed, domain.Command{Path: ps, Args: args})
		} else {
			interpreter := "/bin/sh"
			if s.ID == "brew" {
				interpreter = "/bin/bash"
			}
			allowed = append(allowed, domain.Command{Path: interpreter, Args: []string{installerToken}})
		}
	} else {
		switch s.ID {
		case "mpm":
			args := []string{"tool", "install", "meta-package-manager==" + domain.MPMVersion}
			allowed = append(allowed, e.command("uv", args...), domain.Command{Path: "uv", Args: args})
		case "uv", "mise":
			if e.GOOS == "darwin" {
				allowed = append(allowed, e.command("brew", "install", s.ID), domain.Command{Path: "brew", Args: []string{"install", s.ID}})
			}
			if e.GOOS == "windows" && s.ID == "mise" {
				allowed = append(allowed, e.command("winget", "install", "--id", "jdx.mise", "--exact", "--source", "winget"), domain.Command{Path: "scoop", Args: []string{"install", "mise"}})
				if c, err := e.scoopCommand("install", "mise"); err == nil {
					allowed = append(allowed, c)
				}
			}
		}
	}
	for _, c := range allowed {
		if c.Path == s.Command.Path && reflect.DeepEqual(c.Args, s.Command.Args) {
			return nil
		}
	}
	return fmt.Errorf("%s setup command changed since review or is unsupported", s.ID)
}

// Execute applies the already-reviewed plan. It does not prompt or change the
// parent environment. Independent steps continue after a failure.
func (e *Engine) Execute(ctx context.Context, plan domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	result := domain.ActionResult{Steps: []domain.StepResult{}}
	if err := e.validatePlan(plan); err != nil {
		return result, err
	}
	if out == nil {
		out = io.Discard
	}
	if errout == nil {
		errout = io.Discard
	}
	states := map[string]string{}
	var failures []error
	for _, step := range plan.Steps {
		sr := domain.StepResult{ID: step.ID}
		if err := ctx.Err(); err != nil {
			sr.Status = "cancelled"
			sr.Message = "No new installation started"
			states[step.ID] = sr.Status
			result.Steps = append(result.Steps, sr)
			continue
		}
		for _, dep := range step.DependsOn {
			if states[dep] != "succeeded" && states[dep] != "skipped" {
				sr.Status = "blocked"
				sr.Message = "Prerequisite " + dep + " did not complete"
				failures = append(failures, fmt.Errorf("%s: %s", step.ID, sr.Message))
				break
			}
		}
		if sr.Status == "" && step.GuideURL != "" {
			sr.Status = "needs-attention"
			sr.Message = step.Description + " " + step.GuideURL
			failures = append(failures, errors.New(sr.Message))
		}
		if sr.Status == "" && step.Command.Path == "" && step.URL == "" {
			sr.Status = "skipped"
			sr.Message = "Already installed at review time; no changes requested"
		}
		if sr.Status == "" && e.installed(ctx, step.ID) {
			sr.Status = "skipped"
			sr.Message = "A healthy installation became available after review; kept it"
			if p, err := e.Lookup(targetName(step.ID)); err == nil {
				e.remember(targetName(step.ID), p)
			}
		}
		if sr.Status == "" && step.ID != "mpm" && step.ID != "mpm-standalone" {
			if p, err := e.Lookup(step.ID); err == nil {
				sr.Status = "needs-attention"
				sr.Message = "An existing installation at " + p + " failed its version probe; repair it before setup. " + managerGuide(step.ID)
				failures = append(failures, errors.New(sr.Message))
			}
		}
		if sr.Status == "" {
			fmt.Fprintf(out, "\n%s\n", step.Description)
			if err := e.executeStep(ctx, step, in, out, errout); err != nil {
				sr.Status = "failed"
				sr.Message = err.Error()
				failures = append(failures, fmt.Errorf("%s: %w", step.ID, err))
				fmt.Fprintf(errout, "%s: %v\n", step.ID, err)
			} else {
				sr.Status = "succeeded"
				sr.Message = "Installed and verified"
			}
		}
		states[step.ID] = sr.Status
		result.Steps = append(result.Steps, sr)
	}
	if err := ctx.Err(); err != nil {
		result.Message = "Setup cancelled; completed installations were kept"
		return result, err
	}
	if len(failures) > 0 {
		result.Message = "Setup finished with items requiring attention; successful installations were kept"
		return result, errors.Join(failures...)
	}
	result.Message = "Setup complete. Other terminals may need a new session; mise activation is separate."
	return result, nil
}

func (e *Engine) executeStep(ctx context.Context, s domain.Step, in io.Reader, out, errout io.Writer) error {
	if s.ID == "scoop" {
		if _, err := e.windowsReady(ctx, true); err != nil {
			return err
		}
	}
	if s.URL != "" && s.ID == "mpm-standalone" {
		if err := os.MkdirAll(filepath.Dir(s.Destination), 0700); err != nil {
			return err
		}
		suffix := ".bin"
		if e.GOOS == "windows" {
			suffix = ".exe"
		}
		path, err := e.download(ctx, s.URL, s.Digest, filepath.Dir(s.Destination), suffix, 256*1024*1024)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		if err := os.Chmod(path, 0755); err != nil {
			return err
		}
		// Verify the candidate before replacing an existing app-owned executable.
		if err := e.verify(ctx, "mpm", path); err != nil {
			return err
		}
		if err := os.Rename(path, s.Destination); err != nil {
			return fmt.Errorf("publish verified standalone binary: %w", err)
		}
		e.remember("mpm", s.Destination)
		return nil
	}
	c := s.Command
	c.Args = append([]string(nil), s.Command.Args...)
	if c.Env == nil {
		c.Env = map[string]string{}
	} else {
		copyEnv := map[string]string{}
		for k, v := range c.Env {
			copyEnv[k] = v
		}
		c.Env = copyEnv
	}
	for k, v := range e.ChildEnv() {
		c.Env[k] = v
	}
	if s.URL != "" {
		suffix := ".sh"
		if e.GOOS == "windows" {
			suffix = ".ps1"
		}
		path, err := e.download(ctx, s.URL, "", "", suffix, 8*1024*1024)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		found := false
		for i, a := range c.Args {
			if a == installerToken {
				c.Args[i] = path
				found = true
			}
		}
		if !found {
			return errors.New("installer plan has no explicit script-file argument")
		}
	} else {
		// Dependency installation may have made a previously absent launcher available.
		if c.Path == "scoop" {
			var err error
			c, err = e.scoopCommand(c.Args...)
			if err != nil {
				return err
			}
		} else if filepath.Base(c.Path) == c.Path {
			if p, err := e.Lookup(c.Path); err == nil {
				c.Path = p
			}
		}
	}
	if err := e.Runner.Run(ctx, c, in, out, errout); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.resolveAfter(ctx, s.ID)
	if err != nil {
		return fmt.Errorf("installer completed, but executable was not found: %w", err)
	}
	if err := e.verify(ctx, targetName(s.ID), p); err != nil {
		return fmt.Errorf("installer completed, but verification failed: %w", err)
	}
	e.remember(targetName(s.ID), p)
	return nil
}

func (e *Engine) verify(ctx context.Context, name, path string) error {
	c := domain.Command{Path: path, Args: []string{"--version"}, Env: e.ChildEnv()}
	if name == "scoop" {
		var err error
		c, err = e.scoopCommand("--version")
		if err != nil {
			return err
		}
	}
	res, err := e.output(ctx, c)
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Stdout+res.Stderr) == "" {
		return errors.New("version probe returned no output")
	}
	if versionPattern.FindString(res.Stdout) == "" && !(name == "scoop" && strings.Contains(strings.ToLower(res.Stdout), "scoop")) {
		return errors.New("version probe returned no recognizable version")
	}
	if name == "mpm" && versionPattern.FindString(res.Stdout) != domain.MPMVersion {
		return fmt.Errorf("expected mpm %s, received %q", domain.MPMVersion, strings.TrimSpace(res.Stdout))
	}
	return nil
}
