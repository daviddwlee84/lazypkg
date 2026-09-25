package managedupgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/daviddwlee84/lazypkg/internal/brewupgrade"
	"github.com/spf13/cobra"
	"io"
	"testing"
)

func TestCheckAndParentJSONNeverApply(t *testing.T) {
	root := &cobra.Command{Use: "fixture"}
	root.PersistentFlags().Bool("json", false, "")
	cmd := newCommand(Product{Binary: "fixture"}, func(context.Context) (plan, error) {
		return plan{report: Report{Status: "checked", CanUpgrade: true}, apply: func(context.Context, io.Writer) (brewupgrade.Outcome, error) {
			t.Fatal("check applied")
			return brewupgrade.Outcome{}, nil
		}}, nil
	})
	root.AddCommand(cmd)
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"--json", "upgrade", "--check"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Status != "checked" {
		t.Fatalf("%s: %v", out, err)
	}
}
func TestExplicitApplyReportsActualResultAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			calls := 0
			cmd := newCommand(Product{Binary: "fixture"}, func(context.Context) (plan, error) {
				return plan{report: Report{Status: "checked", CanUpgrade: true}, apply: func(context.Context, io.Writer) (brewupgrade.Outcome, error) {
					calls++
					var err error
					if fail {
						err = errors.New("manager failed")
					}
					return brewupgrade.Outcome{Version: "v1.2.3", Changed: !fail}, err
				}}, nil
			})
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--json", "--yes"})
			err := cmd.Execute()
			if (err != nil) != fail || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			var r Report
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if fail && r.Status != "failed" || !fail && (r.Status != "updated" || r.Version != "v1.2.3") {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestUnconfirmedAndReadOnlyNeverApply(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"--read-only", "--yes"}} {
		cmd := newCommand(Product{}, func(context.Context) (plan, error) {
			return plan{report: Report{CanUpgrade: true}, apply: func(context.Context, io.Writer) (brewupgrade.Outcome, error) {
				t.Fatal("applied")
				return brewupgrade.Outcome{}, nil
			}}, nil
		})
		cmd.Flags().Bool("read-only", false, "")
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if cmd.Execute() == nil {
			t.Fatal("expected refusal")
		}
	}
}
