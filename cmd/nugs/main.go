package main

import (
	"context"
	"os"
	"os/signal"

	nuggets "workspace-nuggets"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(nuggets.RunCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
