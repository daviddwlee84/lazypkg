package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/promptio"
	"github.com/daviddwlee84/lazypkg/internal/promptkit"
	"github.com/spf13/cobra"
)

func resolutionCommand(o *options) *cobra.Command {
	var interactive, dry, yes bool
	var keep, remove string
	cmd := &cobra.Command{Use: "resolve <command>", Short: "Inspect command installations and review one exact removal", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if interactive && (o.json || dry || yes || keep != "" || remove != "") {
			return usageError{fmt.Errorf("--interactive cannot be combined with a plan, approval or JSON output")}
		}
		if interactive && !terminal(cmd) {
			return usageError{fmt.Errorf("--interactive requires a terminal")}
		}
		if (keep == "") != (remove == "") {
			return usageError{fmt.Errorf("specify both --keep and --remove installation IDs")}
		}
		if (dry || yes) && keep == "" {
			return usageError{fmt.Errorf("select exact --keep and --remove installation IDs first")}
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		if interactive {
			return o.runTUI(cmd, s, "resolve:"+args[0])
		}
		if keep != "" {
			p, err := s.PlanResolution(cmd.Context(), domain.ResolutionRequest{Name: args[0], KeepID: keep, RemoveID: remove})
			if err != nil {
				return err
			}
			return apply(cmd, s, p, o.json, dry, yes)
		}
		a, err := s.AssessConflict(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if o.json {
			return writeJSON(cmd.OutOrStdout(), a)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s — independent installation assessment\n", clean(a.Name))
		for _, i := range a.Installations {
			marker := ""
			if i.Effective {
				marker = " · first in PATH"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s / %s %s  [%s]%s\n", i.ID, clean(i.Package.Manager), clean(i.Package.ID), clean(i.Package.Version), i.Status, marker)
			for _, reason := range append(append([]string(nil), i.Blockers...), i.Warnings...) {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", clean(reason))
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Use --interactive, or select --keep ID --remove ID --dry-run to inspect a bound removal plan.")
		return nil
	}}
	cmd.Flags().BoolVar(&interactive, "interactive", false, "Open the guided keep/remove workflow")
	cmd.Flags().StringVar(&keep, "keep", "", "Exact installation ID to retain")
	cmd.Flags().StringVar(&remove, "remove", "", "Exact installation ID to remove")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Show the exact bound plan without changes")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Approve this one reviewed removal")
	return cmd
}
func maintenanceCommand(o *options) *cobra.Command {
	var interactive, dry, refresh bool
	cmd := &cobra.Command{Use: "maintain", Short: "Review manager updates and repairs one at a time", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if interactive && (o.json || dry) {
			return usageError{fmt.Errorf("choose interactive review or a dry-run/JSON queue")}
		}
		if interactive && !terminal(cmd) {
			return usageError{fmt.Errorf("--interactive requires a terminal")}
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		if interactive {
			initial := "maintenance"
			if refresh {
				initial = "maintenance:refresh"
			}
			return o.runTUI(cmd, s, initial)
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		q, err := s.MaintenanceQueue(cmd.Context(), ids, refresh)
		if err != nil {
			return err
		}
		if o.json {
			return writeJSON(cmd.OutOrStdout(), q)
		}
		for _, j := range q.Jobs {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  [%s]  %s\n  %s\n", clean(strings.Join(j.ManagerIDs, ", ")), j.Category, clean(j.Title), clean(j.Reason))
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No changes applied. Use --interactive to confirm each job, or managers upgrade <manager> for a single plan.")
		return nil
	}}
	cmd.Flags().BoolVar(&interactive, "interactive", false, "Review and confirm each job individually")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Print the read-only maintenance queue")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Bypass cached health checks")
	return cmd
}
func promptCommand(o *options) *cobra.Command {
	parent := &cobra.Command{Use: "prompt", Short: "Generate evidence-backed prompts for manual review"}
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.json {
			return writeJSON(cmd.OutOrStdout(), promptkit.Recipes())
		}
		for _, r := range promptkit.Recipes() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", r.Name, r.Summary)
		}
		return nil
	}})
	var copy, refresh bool
	var output string
	render := &cobra.Command{Use: "render <path-conflict|manager-repair> [target]", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		req := domain.PromptRequest{Recipe: args[0], Refresh: refresh}
		if len(args) == 2 {
			req.Target = args[1]
		}
		if err := promptkit.Validate(req); err != nil {
			return usageError{err}
		}
		if req.Recipe == promptkit.ManagerRepair && req.Target != "" && !backend.Known(req.Target) {
			return usageError{fmt.Errorf("unknown manager %q", req.Target)}
		}
		s, err := o.service()
		if err != nil {
			return err
		}
		ids, err := o.selection(cmd.Context(), s)
		if err != nil {
			return err
		}
		if req.Target != "" && req.Recipe == promptkit.ManagerRepair && ids != nil {
			return usageError{fmt.Errorf("choose a manager target or selection flags")}
		}
		if req.Recipe == promptkit.ManagerRepair {
			req.Managers = ids
		}
		p, err := s.RenderPrompt(cmd.Context(), req)
		if err != nil {
			return err
		}
		// Always print the same payload even if a secondary transfer fails.
		if o.json {
			err = writeJSON(cmd.OutOrStdout(), p)
		} else {
			_, err = fmt.Fprint(cmd.OutOrStdout(), p.Markdown)
		}
		if err != nil {
			return err
		}
		if output != "" {
			if err = promptio.Export(output, p.Markdown); err != nil {
				return err
			}
		}
		if copy {
			return promptio.Copy(cmd.Context(), p.Markdown)
		}
		return nil
	}}
	render.Flags().BoolVar(&copy, "copy", false, "Copy exactly the rendered Markdown")
	render.Flags().StringVar(&output, "output", "", "Export Markdown to a new private file (never overwrite)")
	render.Flags().BoolVar(&refresh, "refresh", false, "Refresh manager health while collecting context")
	parent.AddCommand(render)
	return parent
}
