package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/sbx"
	"github.com/bison/worktrunk-sandboxes/internal/wt"
)

// loginShellScript asks the container which shell to run: the sandbox
// user's passwd login shell, falling back to sh when the field is empty
// or getent is unavailable. The host's $SHELL is deliberately not
// consulted -- it names a host path that need not exist in the image.
const loginShellScript = `s="$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f7)"; exec "${s:-sh}" -l`

// enterArgs builds the sbx argv for an interactive shell in sandbox name.
// An explicit shell (flag or config) runs as a login shell; otherwise the
// image's passwd entry decides.
func enterArgs(name, shell string) []string {
	if shell != "" {
		return []string{"exec", "-it", name, shell, "-l"}
	}
	return []string{"exec", "-it", name, "sh", "-c", loginShellScript}
}

func (a *app) newEnterCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "enter [BRANCH [REPO]]",
		Short: "Open an interactive shell in a worktree's existing sandbox",
		Long: "Opens a login shell inside the sandbox for BRANCH (default: the current\n" +
			"branch) via `sbx exec`, landing in the worktree. A stopped sandbox is\n" +
			"started first; a sandbox that does not exist is an error -- enter never\n" +
			"creates one, that's run and create.\n\n" +
			"The shell is, in order: --shell, the `shell` config setting, or the\n" +
			"sandbox user's login shell from the image's /etc/passwd.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() >= 0 {
				return usagef("enter takes no agent args")
			}
			repo, branch, _, err := resolveTarget(args)
			if err != nil {
				return err
			}
			name := wt.SandboxName(repo, branch)
			exists, err := a.sbx.Exists(name)
			if err != nil {
				return err
			}
			if !exists {
				return usagef("no sandbox %s for branch %s; create one with `wt sbx run` or `wt sbx create`", name, branch)
			}
			if shell == "" {
				shell = a.cfg.SettingsFor(repo).Shell
			}
			return sbx.ExecAttach(enterArgs(name, shell))
		},
	}
	cmd.Flags().StringVarP(&shell, "shell", "s", "",
		"shell to run (default: `shell` from config, else the sandbox user's login shell)")
	return cmd
}
