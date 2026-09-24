package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// execution runs with Bubble Tea's terminal released. It is the only TUI path
// that calls Execute; constructing it requires approval of an accepted plan.
type execution struct {
	ctx     context.Context
	service domain.Service
	plan    domain.ActionPlan
	in      io.Reader
	out     io.Writer
	errout  io.Writer
	result  domain.ActionResult
}

func (e *execution) SetStdin(r io.Reader)  { e.in = r }
func (e *execution) SetStdout(w io.Writer) { e.out = w }
func (e *execution) SetStderr(w io.Writer) { e.errout = w }
func (e *execution) Run() error {
	if e.out == nil {
		e.out = io.Discard
	}
	if e.errout == nil {
		e.errout = e.out
	}
	fmt.Fprintln(e.out, "\n"+clean(e.plan.Title)+"\n")
	var err error
	e.result, err = e.service.Execute(e.ctx, e.plan, e.in, e.out, e.errout)
	fmt.Fprintln(e.out, "\n"+clean(resultText(e.result, err)))
	// Acknowledgement belongs to the released terminal, so native output remains
	// visible until the user is ready to return. No concurrent TUI reader exists.
	if e.in != nil && e.ctx.Err() == nil {
		fmt.Fprint(e.out, "\nPress Enter to return to lazypkg. ")
		_, _ = bufio.NewReader(e.in).ReadString('\n')
	}
	return err
}
