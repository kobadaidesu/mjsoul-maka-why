package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"mjcap/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}
