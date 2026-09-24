package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/tui"
	"github.com/spf13/cobra"
)

func (o *options) query(kind, text string) domain.PackageQuery {
	return domain.PackageQuery{Kind: kind, Query: text, Managers: o.managers, Group: o.group, Set: o.set}
}
func (o *options) selection(ctx context.Context, s domain.Service) ([]string, error) {
	if len(o.managers) > 0 {
		ids := make([]string, len(o.managers))
		for i, id := range o.managers {
			ids[i] = backend.NormalizeManager(id)
		}
		return ids, nil
	}
	if o.group == "" && o.set == "" {
		return nil, nil
	}
	prefs, err := s.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	if o.group != "" {
		if ids, ok := prefs.Groups[o.group]; ok {
			return append([]string(nil), ids...), nil
		}
		return nil, usageError{fmt.Errorf("unknown group %q", o.group)}
	}
	if ids, ok := prefs.Sets[o.set]; ok {
		return append([]string(nil), ids...), nil
	}
	return nil, usageError{fmt.Errorf("unknown manager set %q", o.set)}
}
func (o *options) runTUI(cmd *cobra.Command, s domain.Service, initial string) error {
	var mouse *bool
	if cmd.Flags().Changed("mouse") {
		mouse = &o.mouse
	}
	ids, err := o.selection(cmd.Context(), s)
	if err != nil {
		return err
	}
	// Explicit CLI scope becomes a session preference without saving configuration.
	if ids != nil {
		s = &sessionService{Service: s, ids: ids}
	}
	return tui.Run(cmd.Context(), s, initial, tui.Options{Mouse: mouse})
}

type sessionService struct {
	domain.Service
	ids []string
}

func (s *sessionService) Query(ctx context.Context, q domain.PackageQuery) (domain.Snapshot, error) {
	// Init starts reads alongside async preferences. Apply the CLI scope at
	// the service boundary so the first response already has the right scope.
	if q.Managers == nil && q.Group == "" && q.Set == "" {
		q.Managers = append([]string(nil), s.ids...)
	}
	return s.Service.Query(ctx, q)
}

func (s *sessionService) Preferences(ctx context.Context) (domain.ManagerPreferences, error) {
	p, err := s.Service.Preferences(ctx)
	p.Default = append([]string(nil), s.ids...)
	p.Order = append([]string(nil), s.ids...)
	p.DefaultSet = ""
	return p, err
}
func installedLabel(p domain.Package) string {
	if !p.Candidate {
		return p.Version
	}
	switch p.InstallState {
	case "installed":
		v := strings.Join(p.InstalledVersions, ", ")
		if v == "" {
			v = "yes; version not reported"
		}
		if p.InventoryStale {
			v += " (stale)"
		}
		return v
	case "not_installed":
		return "not via this provider"
	case "checking":
		return "checking"
	case "check_failed":
		return "check failed"
	case "unavailable":
		return "provider unavailable"
	default:
		return "not checked"
	}
}
func managerCommands(o *options) *cobra.Command {
	var detected bool
	cmd := &cobra.Command{Use: "managers", Short: "Discover managers, compatibility, scopes and maintenance", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		rows, err := s.Managers(cmd.Context())
		if err != nil {
			return err
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, id := range ids {
			allowed[id] = true
		}
		filtered := rows[:0]
		for _, m := range rows {
			if len(allowed) > 0 && !allowed[m.ID] {
				continue
			}
			if detected && m.Path == "" {
				continue
			}
			filtered = append(filtered, m)
		}
		rows = filtered
		if o.json {
			return writeJSON(cmd.OutOrStdout(), rows)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MANAGER\tVERSION\tREQUIRES\tSTATUS\tSCOPE\tOPERATIONS")
		for _, m := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, clean(m.Version), clean(m.Requirement), clean(m.Status), m.Scope, strings.Join(m.Capabilities, ", "))
		}
		return w.Flush()
	}}
	cmd.Flags().BoolVar(&detected, "detected", false, "Only show managers with an executable")
	var refresh bool
	check := &cobra.Command{Use: "check [manager]", Short: "Check manager update sources (cached for 24h; no upgrades)", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		if len(args) > 0 {
			if ids != nil {
				return usageError{fmt.Errorf("use either a manager argument or selection flags")}
			}
			ids = []string{args[0]}
		}
		rows, err := s.CheckManagers(cmd.Context(), ids, refresh)
		if err != nil {
			return err
		}
		if o.json {
			return writeJSON(cmd.OutOrStdout(), rows)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MANAGER\tVERSION\tREQUIRES\tUPDATE\tCANDIDATE\tOWNER")
		for _, h := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Manager, clean(h.Version), clean(h.Requirement), clean(h.UpdateStatus), clean(h.CandidateVersion), clean(h.Owner))
		}
		w.Flush()
		for _, h := range rows {
			if h.Recommendation != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", h.Manager, clean(h.Recommendation))
			}
		}
		return nil
	}}
	check.Flags().BoolVar(&refresh, "refresh", false, "Bypass cached update checks")
	cmd.AddCommand(check)
	var dry, yes bool
	upgrade := &cobra.Command{Use: "upgrade <manager>", Short: "Review and update one manager through its proven owner", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(o.managers) > 0 || o.group != "" || o.set != "" {
			return usageError{fmt.Errorf("choose the manager with the positional argument, not a query filter")}
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		p, err := s.PlanManagerUpdate(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return apply(cmd, s, p, o.json, dry, yes)
	}}
	upgrade.Flags().BoolVar(&dry, "dry-run", false, "Show the bound update plan without changing tools")
	upgrade.Flags().BoolVarP(&yes, "yes", "y", false, "Approve the reviewed manager update")
	cmd.AddCommand(upgrade)
	return cmd
}
func setCommands(o *options) *cobra.Command {
	list := func(cmd *cobra.Command, _ []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		p, err := s.Preferences(cmd.Context())
		if err != nil {
			return err
		}
		if o.json {
			return writeJSON(cmd.OutOrStdout(), p)
		}
		keys := []string{}
		for k := range p.Sets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			marker := ""
			if k == p.DefaultSet {
				marker = " (default)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s%s: %s\n", k, marker, strings.Join(p.Sets[k], " → "))
		}
		if len(keys) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No saved sets. Example: lazypkg sets save daily --manager brew,mise,uvx")
		}
		return nil
	}
	cmd := &cobra.Command{Use: "sets", Short: "Inspect and save ordered manager selections", Args: cobra.NoArgs, RunE: list}
	cmd.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: list})
	var makeDefault bool
	save := &cobra.Command{Use: "save <name>", Short: "Save the selected manager IDs without rewriting unrelated settings", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := o.service()
		if err != nil {
			return err
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return usageError{fmt.Errorf("select managers using --manager, --group or --set")}
		}
		p, err := s.SaveManagerSet(cmd.Context(), args[0], ids, makeDefault)
		if err != nil {
			return err
		}
		if o.json {
			return writeJSON(cmd.OutOrStdout(), p)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved %s: %s\n", clean(args[0]), strings.Join(p.Sets[args[0]], " → "))
		return nil
	}}
	save.Flags().BoolVar(&makeDefault, "default", false, "Use this set as the default for subsequent queries")
	cmd.AddCommand(save)
	return cmd
}
