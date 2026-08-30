package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) newSweepCmd() *cobra.Command {
	var after time.Duration
	cmd := &cobra.Command{
		Use:    "sweep",
		Short:  "Revoke expired grants (timers and hooks call this)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if after > 0 {
				time.Sleep(after)
			}
			return a.engine(os.Stdout).Sweep()
		},
	}
	cmd.Flags().DurationVar(&after, "after", 0, "sleep this long before sweeping")
	return cmd
}

// spawnDetachedSweep arms the expiry timer for a fresh grant: a detached
// `wt-sbx sweep --after DELAY`, no daemon. A reboot can kill it -- then the
// opportunistic sweep on the next invocation picks the grant up, and status
// shows it OVERDUE in the meantime.
func spawnDetachedSweep(delay time.Duration) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "sweep", "--after", delay.String())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wt-sbx: could not arm expiry timer: %v\n", err)
		return
	}
	_ = cmd.Process.Release()
}
