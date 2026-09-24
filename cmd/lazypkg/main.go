package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/daviddwlee84/lazypkg/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cli.NewRoot().ExecuteContext(ctx)
	cancel()
	if err != nil {
		cli.WriteError(os.Stderr, err)
	}
	os.Exit(cli.ExitCode(err))
}
