// wt-sbx manages Docker Sandboxes bound to worktrunk-managed git worktrees.
// Dispatched by worktrunk git-style, it is reachable as `wt sbx`.
package main

import (
	"os"

	"github.com/bison/worktrunk-sandboxes/internal/cmd"
)

func main() {
	os.Exit(cmd.Run())
}
