// Command relic is the Relic artifact repository: the server, the worker,
// and a client of the Relic API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx, cancel := commandContext(ctx, os.Args[1:])
	defer cancel()

	root := newRootCmd(os.Getenv)
	if err := root.ExecuteContext(ctx); err != nil {
		if ec, ok := errors.AsType[exitCode](err); ok {
			return int(ec)
		}

		_, _ = fmt.Fprintln(os.Stderr, "relic:", err)

		return 1
	}

	return 0
}

const completionTimeout = 5 * time.Second

func commandContext(ctx context.Context, args []string) (context.Context, context.CancelFunc) {
	if len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd) {
		return context.WithTimeout(ctx, completionTimeout)
	}

	return context.WithCancel(ctx)
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
