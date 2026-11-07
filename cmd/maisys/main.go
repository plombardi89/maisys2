// Command maisys sets up and maintains machines from a config repo.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"

	"github.com/plombardi89/maisys2/internal/cli"
	"github.com/plombardi89/maisys2/internal/selfupdate"
	"github.com/plombardi89/maisys2/internal/system"
)

func main() {
	// Delete the binary an earlier self-update moved aside, unless this is
	// the elevated helper, which leaves the user's files alone: it may run
	// as another user.
	if runtime.GOOS == "windows" && !isHelper(os.Args[1:]) {
		if exe, err := selfupdate.Executable(); err == nil {
			selfupdate.RemoveOld(exe)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	abort := make(chan struct{})
	stop := handleInterrupts(cancel, abort)
	code := cli.Execute(ctx, abort, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	cancel()
	os.Exit(code)
}

// isHelper reports whether args, the command line without the program name,
// run the elevated helper.
func isHelper(args []string) bool {
	return len(args) > 0 && args[0] == system.HelperCommand
}

// handleInterrupts calls cancel on the first Ctrl-C, so maisys stops after
// the step in progress, and closes abort on the second, so it stops that
// step too. After that, Ctrl-C ends maisys at once, as it normally would.
// The function it returns stops handling Ctrl-C.
func handleInterrupts(cancel context.CancelFunc, abort chan<- struct{}) (stop func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer signal.Stop(signals)
		select {
		case <-signals:
			cancel()
		case <-done:
			return
		}
		select {
		case <-signals:
			close(abort)
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
