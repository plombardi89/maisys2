// Command fixture builds the config repo the end-to-end tests sync test
// machines from (see test/fixture) in a folder, to look at or to sync by
// hand:
//
//	go run ./test/cmd/fixture DIR
//
// DIR must be empty or not exist yet.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/plombardi89/maisys2/test/fixture"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" || os.Args[1][0] == '-' {
		fmt.Fprintln(os.Stderr, "usage: go run ./test/cmd/fixture DIR")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := fixture.Build(context.Background(), dir); err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("fixture: built the end-to-end tests' config repo in %s\n", dir)
}
