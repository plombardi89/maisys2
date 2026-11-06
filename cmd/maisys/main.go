// Command maisys sets up and maintains machines from a config repo.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/plombardi89/maisys2/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
