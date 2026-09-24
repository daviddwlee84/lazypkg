package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/app"
	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/spf13/cobra"
)

func batchUpgradeCommand(o *options) *cobra.Command {
	var targets []string
	var from, filter string
	var dry, yes bool
	cmd := &cobra.Command{Use: "upgrade-batch", Short: "Review one frozen package selection, then upgrade items sequentially", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if from != "installed" && from != "updates" {
			return usageError{fmt.Errorf("--from must be installed or updates")}
		}
		if len(targets) > 0 && (o.managers != nil || o.group != "" || o.set != "" || cmd.Flags().Changed("from") || cmd.Flags().Changed("filter")) {
			return usageError{fmt.Errorf("use --target entries or a view/filter/manager selection, not both")}
		}
		if cmd.Flags().Changed("target") && len(targets) == 0 {
			return usageError{fmt.Errorf("--target requires manager:package-id")}
		}
		type selectedTarget struct{ manager, id string }
		var exact []selectedTarget
		var managers []string
		seen := map[string]bool{}
		for _, value := range targets {
			manager, id, ok := strings.Cut(value, ":")
			manager = backend.NormalizeManager(manager)
			if !ok || id == "" {
				return usageError{fmt.Errorf("invalid target %q; use manager:package-id", value)}
			}
			if err := app.Validate(domain.ActionRequest{Operation: "upgrade", Manager: manager, Package: id}); err != nil {
				return usageError{err}
			}
			exact = append(exact, selectedTarget{manager, id})
			if !seen[manager] {
				managers = append(managers, manager)
				seen[manager] = true
			}
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		kind := "installed"
		if from == "updates" && len(exact) == 0 {
			kind = "outdated"
		}
		query := o.query(kind, "")
		query.Refresh = true
		if len(exact) > 0 {
			query.Managers = managers
			query.Group = ""
			query.Set = ""
		}
		snapshot, err := s.Query(cmd.Context(), query)
		if err != nil {
			return err
		}
		batchSelectionIssues(cmd.ErrOrStderr(), snapshot)
		request := domain.BatchUpgradeRequest{Source: from, Targets: []domain.Package{}}
		if len(exact) > 0 {
			request.Source = "explicit targets"
			for _, target := range exact {
				found := false
				for _, p := range snapshot.Packages {
					if p.Manager == target.manager && domain.NormalizePackageID(p.Manager, p.ID) == domain.NormalizePackageID(target.manager, target.id) {
						request.Targets = append(request.Targets, p)
						found = true
					}
				}
				if !found {
					return fmt.Errorf("no verified installed record for %s:%s; inspect inventory coverage", target.manager, target.id)
				}
			}
		} else {
			for _, p := range snapshot.Packages {
				if batchFilterMatches(p, filter, kind) {
					request.Targets = append(request.Targets, p)
				}
			}
		}
		plan, err := s.PlanBatchUpgrade(cmd.Context(), request)
		if err != nil {
			return err
		}
		if len(request.Targets) == 0 && batchSelectionIncomplete(snapshot) {
			message := "Selection collection is incomplete: provider reads failed, so an empty target list does not establish that packages are current. No operations ran."
			if o.json {
				var output any = batchPlanOutput{BatchUpgradePlan: plan, SelectionCoverage: snapshot.Coverage, SelectionIssues: snapshot.Issues}
				if !dry {
					output = batchResultOutput{BatchUpgradeResult: domain.BatchUpgradeResult{Entries: []domain.BatchUpgradeItemResult{}, Paused: true, Message: message}, SelectionCoverage: snapshot.Coverage, SelectionIssues: snapshot.Issues}
				}
				if err := writeJSON(cmd.OutOrStdout(), output); err != nil {
					return err
				}
			} else {
				ShowBatchPlan(cmd.OutOrStdout(), plan)
				fmt.Fprintln(cmd.OutOrStdout(), message)
			}
			return fmt.Errorf("cannot determine upgrade targets from incomplete provider inventory; retry or narrow the manager selection")
		}
		if dry {
			if o.json {
				return writeJSON(cmd.OutOrStdout(), batchPlanOutput{BatchUpgradePlan: plan, SelectionCoverage: snapshot.Coverage, SelectionIssues: snapshot.Issues})
			}
			ShowBatchPlan(cmd.OutOrStdout(), plan)
			return nil
		}
		planned := 0
		for _, entry := range plan.Entries {
			if entry.State == "planned" {
				planned++
			}
		}
		if planned > 0 && !yes && (o.json || !terminal(cmd)) {
			return usageError{fmt.Errorf("confirmation required: inspect --dry-run, then supply --yes")}
		}
		ShowBatchPlan(cmd.ErrOrStderr(), plan)
		if planned > 0 && !yes {
			fmt.Fprint(cmd.ErrOrStderr(), "Apply this frozen batch? [y/N] ")
			line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			if strings.ToLower(strings.TrimSpace(line)) != "y" {
				return context.Canceled
			}
		}
		in := batchNativeInput(o.json, terminal(cmd), cmd.InOrStdin())
		result, err := s.ExecuteBatchUpgrade(cmd.Context(), plan, in, cmd.ErrOrStderr(), cmd.ErrOrStderr())
		if o.json {
			if e := writeJSON(cmd.OutOrStdout(), batchResultOutput{BatchUpgradeResult: result, SelectionCoverage: snapshot.Coverage, SelectionIssues: snapshot.Issues}); e != nil {
				return e
			}
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), clean(result.Message))
			for _, entry := range result.Entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s / %s: %s %s\n", entry.Entry.Package.Manager, clean(entry.Entry.Package.ID), entry.State, clean(entry.Message))
			}
		}
		return err
	}}
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Exact manager:package-id (repeat to add targets)")
	cmd.Flags().StringVar(&from, "from", "updates", "Source list: installed or updates")
	cmd.Flags().StringVar(&filter, "filter", "", "Case-insensitive substring filter across list rows")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Show all target plans and exclusions without changing packages")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Approve this frozen batch; failures pause remaining items")
	return cmd
}

func batchFilterMatches(p domain.Package, query, kind string) bool {
	version := p.Version
	if version == "" {
		version = "?"
	}
	if kind == "outdated" {
		latest := p.Latest
		if latest == "" {
			latest = "unknown"
		}
		version += " → " + latest
	}
	if p.InventoryStale {
		version += " (stale)"
	}
	text := strings.Join([]string{p.ID, version, p.Manager, p.Name, p.Description, strings.Join(p.Commands, " ")}, " ")
	return strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query)))
}

// JSON output is a noninteractive contract even when launched from a terminal.
func batchNativeInput(jsonMode, interactive bool, in io.Reader) io.Reader {
	if jsonMode || !interactive {
		return nil
	}
	return in
}

func ShowBatchPlan(w io.Writer, p domain.BatchUpgradePlan) {
	fmt.Fprintf(w, "Package batch: %d target(s) · %s\n", len(p.Entries), clean(p.Request.Source))
	for _, entry := range p.Entries {
		fmt.Fprintf(w, "[%s] %s / %s · %s\n", entry.State, entry.Package.Manager, clean(entry.Package.ID), clean(entry.Reason))
		observed := strings.Join(entry.ObservedVersions, ", ")
		if observed == "" {
			observed = entry.Package.Version
		}
		if observed == "" {
			observed = "not reported"
		}
		target := entry.TargetVersion
		if target == "" {
			target = entry.Package.Latest
		}
		if target == "" {
			target = "not reported"
		}
		fmt.Fprintf(w, "  Installed: %s → candidate: %s\n", clean(observed), clean(target))
		if entry.State == "planned" {
			if entry.Package.Manager == "mise" {
				fmt.Fprintln(w, "  Version policy: exact mise target pinned; activation is a separate action.")
			} else {
				fmt.Fprintln(w, "  Version policy: candidate observed above; the native manager chooses the installed version under its update policy.")
			}
		}
		if entry.Plan != nil {
			ShowPlan(w, *entry.Plan)
		}
	}
}

// Selection evidence is CLI metadata, outside the sealed executable plan. The
// embedded domain fields preserve the existing plan/result JSON shape.
type batchPlanOutput struct {
	domain.BatchUpgradePlan
	SelectionCoverage []domain.Coverage `json:"selection_coverage,omitempty"`
	SelectionIssues   []domain.Issue    `json:"selection_issues,omitempty"`
}
type batchResultOutput struct {
	domain.BatchUpgradeResult
	SelectionCoverage []domain.Coverage `json:"selection_coverage,omitempty"`
	SelectionIssues   []domain.Issue    `json:"selection_issues,omitempty"`
}

func batchSelectionIncomplete(snapshot domain.Snapshot) bool {
	states := map[string]string{}
	for _, coverage := range snapshot.Coverage {
		states[coverage.Manager] = coverage.State
		if coverage.State == "failed" || coverage.State == "pending" {
			return true
		}
	}
	for _, issue := range snapshot.Issues {
		if issue.Kind == "unsupported" || issue.Kind == "excluded" || issue.Kind == "unavailable" {
			continue
		}
		if issue.Kind == "failed" || issue.Kind == "error" {
			return true
		}
		if _, covered := states[issue.Manager]; !covered {
			return true
		}
	}
	return false
}
func batchSelectionIssues(w io.Writer, snapshot domain.Snapshot) {
	seen := map[string]bool{}
	for _, issue := range snapshot.Issues {
		key := issue.Manager + "\x00" + issue.Message
		if !seen[key] {
			issues(w, []domain.Issue{issue})
			seen[key] = true
		}
	}
	for _, coverage := range snapshot.Coverage {
		if coverage.State == "complete" {
			continue
		}
		if seen[coverage.Manager+"\x00"+coverage.Message] {
			continue
		}
		fmt.Fprintf(w, "%s: %s %s\n", clean(coverage.Manager), clean(coverage.State), clean(coverage.Message))
		seen[coverage.Manager+"\x00"+coverage.Message] = true
	}
}
