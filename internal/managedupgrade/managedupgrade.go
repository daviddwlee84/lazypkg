// Package managedupgrade provides an explicit, receipt-verified Homebrew update.
// Standalone archives remain externally managed; no source/network fallback runs.
package managedupgrade

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/daviddwlee84/lazypkg/internal/brewupgrade"
	"github.com/spf13/cobra"
)

type Product struct{ Binary, Module, Main string }
type Report struct {
	Status         string   `json:"status"`
	Manager        string   `json:"manager,omitempty"`
	Formula        string   `json:"formula,omitempty"`
	Path           string   `json:"path,omitempty"`
	ResolvedPath   string   `json:"resolved_path,omitempty"`
	CurrentVersion string   `json:"current_version,omitempty"`
	Version        string   `json:"version,omitempty"`
	Command        []string `json:"command,omitempty"`
	CanUpgrade     bool     `json:"can_upgrade"`
	Changed        bool     `json:"changed"`
	Reason         string   `json:"reason,omitempty"`
}
type plan struct {
	report Report
	apply  func(context.Context, io.Writer) (brewupgrade.Outcome, error)
}

// Inspect refuses to execute a different product merely sharing its filename.
func Inspect(product Product) func(context.Context, string) (string, error) {
	return func(ctx context.Context, path string) (string, error) {
		info, err := buildinfo.ReadFile(path)
		if err != nil || info.Main.Path != product.Module || info.Path != product.Main || info.Main.Replace != nil {
			return "", errors.New("installed executable has a different module or main package")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, path, "--version")
		output := &boundedOutput{}
		child.Stdout = output
		child.Stderr = io.Discard
		child.WaitDelay = time.Second
		if err := child.Run(); err != nil {
			return "", fmt.Errorf("installed executable failed version check: %w", err)
		}
		text := strings.TrimSpace(output.String())
		// All suite products use '<binary> [version] vMAJOR.MINOR.PATCH'.
		if !strings.HasPrefix(text, product.Binary+" ") {
			return "", errors.New("unexpected version product")
		}
		version := regexp.MustCompile(`(?:^|\s)(v[0-9]+\.[0-9]+\.[0-9]+)(?:\s|$)`).FindStringSubmatch(text)
		if len(version) != 2 {
			return "", errors.New("installed executable did not report a stable version")
		}
		return version[1], nil
	}
}

type boundedOutput struct{ strings.Builder }

func (w *boundedOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 4096 {
		return 0, errors.New("version output exceeded limit")
	}
	return w.Builder.Write(p)
}

func prepare(ctx context.Context, product Product) (plan, error) {
	executable, err := os.Executable()
	if err != nil {
		return plan{}, err
	}
	p, err := brewupgrade.Prepare(ctx, executable, product.Binary, brewupgrade.Options{Inspect: Inspect(product)})
	if errors.Is(err, brewupgrade.ErrNotManaged) {
		return plan{report: Report{Status: "unsupported", Path: executable, Reason: "This command upgrades verified Homebrew installations. For a chezmoi-managed release use just upgrade-personal; for other installs use their original owner. Manual source: go install " + product.Main + "@latest. Standalone archives: https://github.com/" + strings.TrimPrefix(product.Module, "github.com/") + "/releases. Local builds are preserved."}}, nil
	}
	if err != nil {
		return plan{}, err
	}
	return plan{report: Report{Status: "checked", Manager: "homebrew", Formula: p.Formula, Path: p.StablePath, ResolvedPath: p.CurrentPath, CurrentVersion: p.CurrentVersion, Command: p.Command(), CanUpgrade: true}, apply: p.Apply}, nil
}
func NewCommand(product Product) *cobra.Command {
	return newCommand(product, func(ctx context.Context) (plan, error) { return prepare(ctx, product) })
}
func newCommand(product Product, preparePlan func(context.Context) (plan, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "upgrade", SilenceUsage: true, SilenceErrors: true, Short: "Upgrade this executable through its verified Homebrew owner", Args: cobra.NoArgs}
	cmd.Long = "Check the running executable's owner and delegate one exact Homebrew formula. No backend configuration is loaded; other installation channels receive their supported external update instructions."
	cmd.Flags().Bool("check", false, "Read-only ownership and upgrade-command check")
	cmd.Flags().Bool("yes", false, "Approve this executable's Homebrew upgrade")
	cmd.Flags().Bool("json", false, "Write structured results without prompting")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		p, err := preparePlan(cmd.Context())
		if err != nil {
			return err
		}
		jsonMode := flag(cmd, "json")
		emit := func() error {
			if jsonMode {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(p.report)
			}
			if !p.report.CanUpgrade {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), p.report.Reason)
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\nOwner: Homebrew %s\nCommand: %q\n", p.report.Status, product.Binary, p.report.CurrentVersion, p.report.Formula, p.report.Command)
			return err
		}
		if flag(cmd, "check") || flag(cmd, "dry-run") {
			return emit()
		}
		if flag(cmd, "read-only") {
			return errors.New("read-only mode forbids upgrading; use --check")
		}
		if !p.report.CanUpgrade || p.apply == nil {
			if err := emit(); err != nil {
				return err
			}
			return errors.New(p.report.Reason)
		}
		if !flag(cmd, "yes") {
			if jsonMode || !terminal(cmd.InOrStdin()) || !terminal(cmd.OutOrStdout()) {
				return errors.New("inspect with --check, then pass --yes to approve the upgrade")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Run %q? [y/N] ", p.report.Command)
			if err := confirm(cmd.Context(), cmd.InOrStdin()); err != nil {
				return err
			}
		}
		progress := cmd.ErrOrStderr()
		if jsonMode {
			progress = io.Discard
		}
		outcome, applyErr := p.apply(cmd.Context(), progress)
		p.report.Status = "up-to-date"
		if outcome.Changed {
			p.report.Status = "updated"
		}
		if applyErr != nil {
			p.report.Status = "failed"
			p.report.Reason = applyErr.Error()
		}
		p.report.Changed = outcome.Changed
		p.report.Version = outcome.Version
		if outcome.Path != "" {
			p.report.Path = outcome.Path
		}
		if outcome.ResolvedPath != "" {
			p.report.ResolvedPath = outcome.ResolvedPath
		}
		if jsonMode {
			if err := emit(); err != nil {
				return err
			}
		} else if applyErr == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Homebrew %s: %s %s at %s. Start a new invocation to use it.\n", p.report.Status, product.Binary, outcome.Version, outcome.Path)
			if err != nil {
				return err
			}
		}
		return applyErr
	}
	return cmd
}

// A parent --json/--yes remains effective when supplied before the subcommand.
func flag(cmd *cobra.Command, name string) bool {
	for c := cmd; c != nil; c = c.Parent() {
		for _, set := range []interface {
			Changed(string) bool
			GetBool(string) (bool, error)
		}{c.Flags(), c.PersistentFlags()} {
			if set.Changed(name) {
				v, _ := set.GetBool(name)
				return v
			}
		}
	}
	return false
}
func terminal(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
func confirm(ctx context.Context, input io.Reader) error {
	type response struct {
		line string
		err  error
	}
	ch := make(chan response, 1)
	go func() { s, e := bufio.NewReader(input).ReadString('\n'); ch <- response{s, e} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		if r.err != nil && !errors.Is(r.err, io.EOF) {
			return r.err
		}
		s := strings.TrimSpace(r.line)
		if strings.EqualFold(s, "y") || strings.EqualFold(s, "yes") {
			return nil
		}
		return context.Canceled
	}
}
