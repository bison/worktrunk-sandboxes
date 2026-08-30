package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/grants"
	"github.com/bison/worktrunk-sandboxes/internal/printer"
)

func (a *app) newNetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Ad hoc network access for a branch's sandbox",
		Long: "The interactive verbs for when an agent reports a blocked domain. They ride\n" +
			"the same grant machinery as `auth` (one state file, one expiry path), and\n" +
			"policy rules reach running sandboxes immediately.",
	}
	cmd.AddCommand(
		a.newNetAllowCmd(),
		a.newNetOpenCmd(),
		a.newNetLogCmd(),
	)
	return cmd
}

func (a *app) newNetAllowCmd() *cobra.Command {
	var duration string
	cmd := &cobra.Command{
		Use:   "allow HOST [BRANCH]",
		Short: "Allow one host for a branch's sandbox",
		Args:  cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return a.sbx.RecentBlockedHosts(""), cobra.ShellCompDirectiveNoFileComp
			}
			return completeBranches(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dur, err := grants.ParseDuration(duration)
			if err != nil {
				return usageError{err}
			}
			t, err := a.target(argOr(args, 1))
			if err != nil {
				return err
			}
			return a.engine(os.Stdout).AllowHost(args[0], t, dur)
		},
	}
	cmd.Flags().StringVarP(&duration, "duration", "d", "0",
		"rule lifetime (Ns/Nm/Nh/Nd, default 0 = the sandbox's life)")
	return cmd
}

func (a *app) newNetOpenCmd() *cobra.Command {
	var duration string
	cmd := &cobra.Command{
		Use:   "open [BRANCH]",
		Short: "Open full egress for a branch's sandbox (grants net-all)",
		Long: "Sugar for `auth grant net-all`: time-boxed full egress for research\n" +
			"sessions. Needs a [grants.net-all] entry with network = [\"**\"].",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			dur, err := grants.ParseDuration(duration)
			if err != nil {
				return usageError{err}
			}
			t, err := a.target(argOr(args, 0))
			if err != nil {
				return err
			}
			return a.engine(os.Stdout).Grant("net-all", t, dur)
		},
	}
	cmd.Flags().StringVarP(&duration, "duration", "d", "1h",
		"grant lifetime (Ns/Nm/Nh/Nd, 0 = until revoked)")
	return cmd
}

func (a *app) newNetLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "log [BRANCH]",
		Short:             "Show what the sandbox's traffic hit (allowed and blocked)",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.target(argOr(args, 0))
			if err != nil {
				return err
			}
			raw, err := a.sbx.PolicyLogRaw(t.Sandbox)
			if err != nil {
				return err
			}
			if a.format == "json" {
				lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
				return printer.JSON(os.Stdout, map[string]any{
					"sandbox": t.Sandbox,
					"log":     lines,
				})
			}
			fmt.Print(raw)
			return nil
		},
	}
}
