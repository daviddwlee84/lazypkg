package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/daviddwlee84/lazypkg/internal/app"
	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
	"github.com/spf13/cobra"
)

var Version = "dev"

const BaseVersion = "v0.1.1"

func version() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if b, ok := debug.ReadBuildInfo(); ok && b.Main.Version != "" && b.Main.Version != "(devel)" && !strings.Contains(b.Main.Version, "dirty") {
		return b.Main.Version
	}
	return BaseVersion + "-dev"
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var u usageError
	if errors.As(err, &u) || strings.HasPrefix(err.Error(), "unknown command ") {
		return 2
	}
	return 1
}

type options struct {
	override                domain.Service
	config, mpm, group, set string
	managers                []string
	mouse                   bool
	json                    bool
	timeout                 int
}

func terminal(cmd *cobra.Command) bool {
	in, iok := cmd.InOrStdin().(*os.File)
	out, ook := cmd.OutOrStdout().(*os.File)
	return iok && ook && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		if r == 127 {
			return -1
		}
		return r
	}, s)
}

func WriteError(w io.Writer, err error) { fmt.Fprintln(w, clean(err.Error())) }
func (o *options) service() (domain.Service, error) {
	if o.override != nil {
		return o.override, nil
	}
	c, err := config.Load(o.config)
	if err != nil {
		return nil, usageError{err}
	}
	if o.mpm != "" {
		c.MPMPath = o.mpm
	}
	if o.timeout != 0 {
		if o.timeout < 1 || o.timeout > 600 {
			return nil, usageError{fmt.Errorf("--timeout must be 1..600 seconds")}
		}
		c.TimeoutSeconds = o.timeout
	}
	if _, err := c.Select(o.query("installed", "")); err != nil {
		return nil, usageError{err}
	}
	a := app.New(c)
	a.Version = version()
	return a, nil
}
func NewRoot() *cobra.Command { return newRoot(nil) }
func newRoot(service domain.Service) *cobra.Command {
	o := &options{override: service}
	root := &cobra.Command{Use: "lazypkg", Short: "See, search and manage software across package managers", Version: version(), SilenceErrors: true, SilenceUsage: true, Args: cobra.NoArgs}
	root.AddCommand(resolutionCommand(o), promptCommand(o))
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().StringVar(&o.config, "config", "", "Path to TOML configuration")
	root.PersistentFlags().StringVar(&o.mpm, "mpm", "", "Explicit mpm executable (tested version "+domain.MPMVersion+")")
	root.PersistentFlags().StringSliceVarP(&o.managers, "manager", "m", nil, "Select managers (repeat or comma separate; uv means uv tools)")
	root.PersistentFlags().StringVar(&o.group, "group", "", "Select a built-in manager group")
	root.PersistentFlags().StringVar(&o.set, "set", "", "Select an ordered saved manager set")
	root.PersistentFlags().BoolVar(&o.mouse, "mouse", true, "Enable mouse interaction in the dashboard")
	root.PersistentFlags().BoolVar(&o.json, "json", false, "Emit machine-readable JSON; never prompt")
	root.PersistentFlags().IntVar(&o.timeout, "timeout", 0, "Read command timeout in seconds (1..600)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("timeout") && (o.timeout < 1 || o.timeout > 600) {
			return usageError{fmt.Errorf("--timeout must be 1..600 seconds")}
		}
		selectors := 0
		if len(o.managers) > 0 {
			selectors++
		}
		if o.group != "" {
			selectors++
		}
		if o.set != "" {
			selectors++
		}
		if selectors > 1 {
			return usageError{fmt.Errorf("choose one of --manager, --group or --set")}
		}
		if cmd.Flags().Changed("manager") && len(o.managers) == 0 {
			return usageError{fmt.Errorf("--manager requires at least one ID")}
		}
		for _, id := range o.managers {
			if !backend.Known(backend.NormalizeManager(id)) {
				return usageError{fmt.Errorf("unsupported --manager %q", id)}
			}
		}
		return nil
	}
	_ = root.RegisterFlagCompletionFunc("manager", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return append(append([]string(nil), backend.Core...), "uv"), cobra.ShellCompDirectiveNoFileComp
	})
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if o.json {
			return usageError{fmt.Errorf("--json requires a query command, e.g. list --json")}
		}
		if !terminal(cmd) {
			return cmd.Help()
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		return o.runTUI(cmd, s, "installed")
	}
	for _, q := range []struct{ use, kind, desc string }{{"list [filter]", "installed", "List globally installed packages and tools"}, {"search <query>", "search", "Search available managers; uv tools use exact PyPI names"}, {"updates [filter]", "outdated", "List available updates"}} {
		q := q
		c := &cobra.Command{Use: q.use, Short: q.desc, Args: cobra.MaximumNArgs(1)}
		var refresh bool
		c.Flags().BoolVar(&refresh, "refresh", false, "Refresh provider observations instead of using memory cache")
		if q.kind == "search" {
			c.Args = cobra.ExactArgs(1)
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			s, err := o.service()
			if err != nil {
				return err
			}
			query := ""
			if len(args) > 0 {
				query = args[0]
			}
			request := o.query(q.kind, query)
			request.Refresh = refresh
			v, err := s.Query(cmd.Context(), request)
			if err != nil {
				return err
			}
			if o.json {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PACKAGE\tINSTALLED\tAVAILABLE\tMANAGER\tSCOPE")
			for _, p := range v.Packages {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", clean(p.ID), clean(installedLabel(p)), clean(p.Latest), p.Manager, clean(p.Scope))
			}
			w.Flush()
			issues(cmd.ErrOrStderr(), v.Issues)
			fmt.Fprintf(cmd.ErrOrStderr(), "%d installation/candidate records\n", len(v.Packages))
			return nil
		}
		root.AddCommand(c)
	}
	root.AddCommand(managerCommands(o), setCommands(o))

	diag := &cobra.Command{Use: "diagnose [command]", Short: "Explain executable ownership and PATH shadowing", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		v, err := s.Diagnose(cmd.Context(), name)
		if err != nil {
			return err
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		v = domain.FilterDiagnostics(v, ids)
		if o.json {
			return writeJSON(cmd.OutOrStdout(), v)
		}
		fmt.Fprintln(cmd.OutOrStdout(), clean(v.Scope))
		fmt.Fprintln(cmd.OutOrStdout(), "Directory:", clean(v.Directory))
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "COMMAND\tSTATE\tMANAGER\tPATH → TARGET")
		for _, e := range v.Executables {
			state := "candidate"
			if e.Preferred {
				state = "PATH first"
			}
			if e.EquivalentTo != "" {
				state = "same target"
			}
			if e.Problem != "" {
				state = e.Problem
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s → %s\n", clean(e.Name), clean(state), e.Manager, clean(e.Path), clean(e.Target))
		}
		w.Flush()
		for _, f := range v.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", f.Kind, clean(f.Message))
		}
		issues(cmd.ErrOrStderr(), v.Issues)
		return nil
	}}
	root.AddCommand(diag)
	for _, op := range []string{"install", "remove", "upgrade", "activate"} {
		op := op
		var yes, dry bool
		var ver string
		c := &cobra.Command{Use: op + " <package-id>", Short: op + " one explicitly selected package (activate is mise global only)", Args: cobra.ExactArgs(1)}
		c.Flags().BoolVarP(&yes, "yes", "y", false, "Approve the displayed operation without prompting")
		c.Flags().BoolVar(&dry, "dry-run", false, "Show the real operation plan without executing")
		c.Flags().StringVar(&ver, "version", "", "mise version (required for remove/activate)")
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if len(o.managers) != 1 || o.group != "" || o.set != "" {
				return usageError{fmt.Errorf("--manager is required; example: lazypkg %s ripgrep --manager brew", op)}
			}
			s, err := o.service()
			if err != nil {
				return err
			}
			p, err := s.Plan(cmd.Context(), domain.ActionRequest{Operation: op, Manager: backend.NormalizeManager(o.managers[0]), Package: args[0], Version: ver})
			if err != nil {
				return err
			}
			return apply(cmd, s, p, o.json, dry, yes)
		}
		root.AddCommand(c)
	}
	var setupYes, setupDry, interactive bool
	setup := &cobra.Command{Use: "setup [manager...]", Short: "Select and install managers and the mpm backend", Long: "Select managers with a multiselect wizard, or name them explicitly.\nExamples: lazypkg setup; lazypkg setup mpm mise --dry-run; lazypkg setup mpm --yes", RunE: func(cmd *cobra.Command, args []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		if interactive && (o.json || !terminal(cmd)) {
			return usageError{fmt.Errorf("--interactive requires a terminal and cannot be combined with --json")}
		}
		if len(args) == 0 {
			if setupYes || setupDry || o.json || !terminal(cmd) {
				v, e := s.SetupOptions(cmd.Context())
				if e != nil {
					return e
				}
				if o.json {
					return writeJSON(cmd.OutOrStdout(), v)
				}
				for _, v := range v {
					fmt.Fprintf(cmd.OutOrStdout(), "%-16s %s\n", v.ID, clean(v.Description))
				}
				return usageError{fmt.Errorf("name setup items or run lazypkg setup in a terminal")}
			}
			return o.runTUI(cmd, s, "setup")
		}
		if interactive {
			return usageError{fmt.Errorf("use bare setup --interactive for the selection wizard, or explicit IDs without --interactive")}
		}
		p, err := s.PlanSetup(cmd.Context(), args)
		if err != nil {
			return err
		}
		return apply(cmd, s, p, o.json, setupDry, setupYes)
	}}
	setup.Flags().BoolVarP(&setupYes, "yes", "y", false, "Approve this installation plan without prompting")
	setup.Flags().BoolVar(&setupDry, "dry-run", false, "Preview the setup plan")
	setup.Flags().BoolVar(&interactive, "interactive", false, "Open the manager selection wizard")
	root.AddCommand(setup)
	configCmd := &cobra.Command{Use: "config", Short: "Inspect effective configuration"}
	configCmd.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := config.Load(o.config)
		if err != nil {
			return err
		}
		if o.mpm != "" {
			c.MPMPath = o.mpm
		}
		if o.timeout != 0 {
			c.TimeoutSeconds = o.timeout
		}
		return writeJSON(cmd.OutOrStdout(), c)
	}})
	root.AddCommand(configCmd)
	var wrapArgs func(*cobra.Command)
	wrapArgs = func(c *cobra.Command) {
		if validate := c.Args; validate != nil {
			c.Args = func(cmd *cobra.Command, args []string) error {
				if err := validate(cmd, args); err != nil {
					return usageError{err}
				}
				return nil
			}
		}
		for _, child := range c.Commands() {
			wrapArgs(child)
		}
	}
	wrapArgs(root)
	return root
}
func issues(w io.Writer, v []domain.Issue) {
	for _, i := range v {
		fmt.Fprintf(w, "%s: %s\n", i.Manager, clean(i.Message))
	}
}
func ShowPlan(w io.Writer, p domain.ActionPlan) {
	fmt.Fprintln(w, clean(p.Title))
	for _, s := range p.Steps {
		fmt.Fprintf(w, "  %s: %s\n", s.ID, clean(s.Description))
		if s.Command.Path != "" {
			fmt.Fprintln(w, "    "+clean(process.Display(s.Command)))
		}
		if s.URL != "" {
			fmt.Fprintln(w, "    Download:", clean(s.URL))
		}
		if s.GuideURL != "" {
			fmt.Fprintln(w, "    Guide:", clean(s.GuideURL))
		}
	}
	if p.Preview != "" {
		fmt.Fprintln(w, clean(p.Preview))
	}
	for _, s := range p.Warnings {
		fmt.Fprintln(w, "Note:", clean(s))
	}
}
func apply(cmd *cobra.Command, s domain.Service, p domain.ActionPlan, jsonMode, dry, yes bool) error {
	if dry {
		if jsonMode {
			return writeJSON(cmd.OutOrStdout(), p)
		}
		ShowPlan(cmd.OutOrStdout(), p)
		return nil
	}
	if p.ManagerUpdate != nil && !p.ManagerUpdate.ApplySupported {
		if jsonMode {
			if err := writeJSON(cmd.OutOrStdout(), p); err != nil {
				return err
			}
		} else {
			ShowPlan(cmd.OutOrStdout(), p)
		}
		return fmt.Errorf("this manager has guidance only; no update was executed")
	}
	if !yes && (jsonMode || !terminal(cmd)) {
		return usageError{fmt.Errorf("confirmation required: inspect --dry-run, then supply --yes")}
	}
	ShowPlan(cmd.ErrOrStderr(), p)
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Apply this plan? [y/N] ")
		answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			return context.Canceled
		}
	}
	var in io.Reader = cmd.InOrStdin()
	if !terminal(cmd) {
		in = nil
	}
	r, err := s.Execute(cmd.Context(), p, in, cmd.ErrOrStderr(), cmd.ErrOrStderr())
	if jsonMode {
		if e := writeJSON(cmd.OutOrStdout(), r); e != nil {
			return e
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), clean(r.Message))
		for _, step := range r.Steps {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s\n", step.ID, step.Status, clean(step.Message))
		}
	}
	return err
}
