package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/grants"
	"github.com/bison/worktrunk-sandboxes/internal/printer"
	"github.com/bison/worktrunk-sandboxes/internal/state"
)

func (a *app) newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Time-boxed credential and network grants for sandboxes",
		Long: "The global sbx secret store carries read-only baselines (see `auth init`);\n" +
			"a grant scopes a more capable secret -- or extra network access -- to one\n" +
			"sandbox, and a revoke removes it again, both taking effect on running\n" +
			"sandboxes immediately. Grants are named in the config file and resolved\n" +
			"at grant time.",
	}
	cmd.AddCommand(
		a.newAuthGrantCmd(),
		a.newAuthRevokeCmd(),
		a.newAuthStatusCmd(),
		a.newAuthInitCmd(),
	)
	return cmd
}

func (a *app) completeGrantNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return a.cfg.GrantNames(), cobra.ShellCompDirectiveNoFileComp
}

func (a *app) newAuthGrantCmd() *cobra.Command {
	var duration string
	cmd := &cobra.Command{
		Use:   "grant NAME [BRANCH]",
		Short: "Grant a named capability to a branch's sandbox",
		Args:  cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return a.completeGrantNames(cmd, args, tc)
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
			return a.engine(os.Stdout).Grant(args[0], t, dur)
		},
	}
	cmd.Flags().StringVarP(&duration, "duration", "d", "1h",
		"grant lifetime (Ns/Nm/Nh/Nd, 0 = until revoked)")
	return cmd
}

func (a *app) newAuthRevokeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "revoke NAME|--all [BRANCH]",
		Short: "Revoke a grant (or all grants) from a branch's sandbox",
		Long: "Revocation reaches running sandboxes: the live secret value is overwritten\n" +
			"with the read-only baseline (or garbage -- fail closed) before the scoped\n" +
			"row is removed; network rules are removed directly. `revoke --all` with\n" +
			"nothing to revoke exits 0: the worktrunk post-merge/post-remove hooks call\n" +
			"it unconditionally.",
		Args: cobra.RangeArgs(0, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 && !all {
				return a.completeGrantNames(cmd, args, tc)
			}
			return completeBranches(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			eng := a.engine(os.Stdout)
			if all {
				t, err := a.target(argOr(args, 0))
				if err != nil {
					return err
				}
				return eng.RevokeAll(t)
			}
			if len(args) < 1 {
				return usagef("revoke needs a grant NAME or --all")
			}
			t, err := a.target(argOr(args, 1))
			if err != nil {
				return err
			}
			return eng.Revoke(args[0], t)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "revoke every grant on the sandbox")
	return cmd
}

func (a *app) newAuthStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show active grants and their expiries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := a.store.Load()
			if err != nil {
				return err
			}
			now := time.Now()
			if a.format == "json" {
				return printer.JSON(os.Stdout, grantsJSON(rows, now))
			}
			if len(rows) == 0 {
				fmt.Println("no active grants")
			} else {
				var table [][]string
				for _, r := range rows {
					table = append(table, []string{
						r.Name, r.Sandbox, r.Branch,
						grants.FormatExpiry(r.Expiry, now),
					})
				}
				printer.Table(os.Stdout, []string{"GRANT", "SANDBOX", "BRANCH", "EXPIRES"}, table)
			}
			if raw, err := a.sbx.SecretListRaw(); err == nil {
				fmt.Println()
				fmt.Print(raw)
			}
			return nil
		},
	}
	return cmd
}

func (a *app) newAuthInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Install the global baseline: network allowlist and read-only secrets",
		Long: "Applies the [network] allow delta on top of sbx's global preset\n" +
			"(idempotently) and installs each [default.<service>] secret as a dynamic\n" +
			"source, so sbx's refresh cycle keeps the read-only baseline current.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.engine(os.Stdout).Init()
		},
	}
}

// grantJSON is the stable JSON shape for one grant row (docs/json.md).
type grantJSON struct {
	Name      string  `json:"name"`
	Sandbox   string  `json:"sandbox"`
	Branch    string  `json:"branch"`
	Repo      string  `json:"repo,omitempty"`
	ExpiresAt *string `json:"expires_at"` // ISO-8601, null = no expiry
	Overdue   bool    `json:"overdue"`
}

func grantsJSON(rows []state.Grant, now time.Time) map[string][]grantJSON {
	out := make([]grantJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, oneGrantJSON(r, now))
	}
	return map[string][]grantJSON{"grants": out}
}

func oneGrantJSON(r state.Grant, now time.Time) grantJSON {
	g := grantJSON{
		Name:    r.Name,
		Sandbox: r.Sandbox,
		Branch:  r.Branch,
		Repo:    r.Repo,
		Overdue: r.Expired(now),
	}
	if r.Expiry != 0 {
		s := time.Unix(r.Expiry, 0).UTC().Format(time.RFC3339)
		g.ExpiresAt = &s
	}
	return g
}

func argOr(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}
