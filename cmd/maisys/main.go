// Command maisys sets up and maintains machines from a config repo.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"

	"github.com/plombardi89/maisys2/internal/cli"
	"github.com/plombardi89/maisys2/internal/selfupdate"
)

func main() {
	if runtime.GOOS == "windows" {
		// Delete the binary an earlier self-update moved aside.
		if exe, err := selfupdate.Executable(); err == nil {
			selfupdate.RemoveOld(exe)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
