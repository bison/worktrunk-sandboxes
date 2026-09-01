package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/sbx"
	"github.com/bison/worktrunk-sandboxes/internal/wt"
)

// launchFlags are shared by run and create; defaults come from config.
type launchFlags struct {
	agent    string
	model    string
	template string
	env      []string
}

func (a *app) addLaunchFlags(cmd *cobra.Command, f *launchFlags) {
	cmd.Flags().StringVarP(&f.agent, "agent", "a", "claude", "agent to run")
	cmd.Flags().StringVarP(&f.model, "model", "m", "",
		"model (sets ANTHROPIC_DEFAULT_MODEL; empty string disables; default from config)")
	cmd.Flags().StringVarP(&f.template, "template", "t", "", "sandbox template image")
	cmd.Flags().StringArrayVarP(&f.env, "env", "e", nil, "environment variable KEY=VALUE (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("agent",
		cobra.FixedCompletions(sbx.Agents(), cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("template",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return a.sbx.TemplateNames(), cobra.ShellCompDirectiveNoFileComp
		})
}

// launch is a fully resolved session: everything needed to build the sbx
// argv for a create or an attach.
type launch struct {
	name      string
	agent     string
	template  string // empty = untemplated
	env       []string
	mounts    []wt.Mount // nil when re-attaching (workspaces fixed at create)
	exists    bool
	agentArgs []string
}

// resolveLaunch turns [BRANCH [REPO]] plus flags into a launch. BRANCH
// defaults to the current branch, REPO to the main checkout of cwd (or of
// REPO when given), resolved through the shared .git so it works from
// inside any worktree.
func (a *app) resolveLaunch(f *launchFlags, cmd *cobra.Command, args []string) (*launch, error) {
	positional := args
	var agentArgs []string
	if i := cmd.ArgsLenAtDash(); i >= 0 {
		positional, agentArgs = args[:i], args[i:]
	}
	if len(positional) > 2 {
		return nil, usagef("at most BRANCH and REPO expected, got %d arguments", len(positional))
	}
	repoArg := "."
	if len(positional) >= 2 {
		repoArg = positional[1]
	}
	repo, err := wt.MainRepo(repoArg)
	if err != nil {
		return nil, err
	}
	var branch, prSrc string
	if len(positional) >= 1 {
		branch = positional[0]
		if pr, ok := wt.ParsePR(branch); ok {
			// The sandbox is named after the head branch, so resolve it
			// up front; worktree creation still goes through wt with the
			// original reference.
			prSrc = branch
			if branch, err = pr.HeadBranch(repo); err != nil {
				return nil, err
			}
		}
	} else if branch, err = wt.CurrentBranch("."); err != nil {
		return nil, err
	}

	settings := a.cfg.SettingsFor(repo)
	model := settings.DefaultModel
	if cmd.Flags().Changed("model") {
		model = f.model
	}
	env := f.env
	if model != "" {
		// ANTHROPIC_DEFAULT_MODEL, never ANTHROPIC_MODEL: the latter
		// overrides resumed sessions' saved models and saved /model
		// choices on every attach.
		env = append([]string{"ANTHROPIC_DEFAULT_MODEL=" + model}, env...)
	}

	l := &launch{
		name:      wt.SandboxName(repo, branch),
		agent:     f.agent,
		env:       env,
		agentArgs: agentArgs,
	}
	if l.exists, err = a.sbx.Exists(l.name); err != nil {
		return nil, err
	}
	if l.exists {
		return l, nil
	}

	wtPath, err := wt.EnsureWorktreeFrom(repo, branch, prSrc)
	if err != nil {
		return nil, err
	}
	extra, err := wt.ExpandMounts(settings.ExtraMounts, repo, wtPath, branch)
	if err != nil {
		return nil, err
	}
	if l.mounts, err = wt.LaunchMounts(extra, repo, wtPath); err != nil {
		return nil, err
	}

	// `sbx template load` does not make an image the agent's default;
	// without --template sbx runs the upstream image (no wt, no config).
	// Map known agents onto the configured registry; pass unknown agents
	// through untemplated.
	l.template = f.template
	if l.template == "" && settings.TemplateRegistry != "" {
		switch f.agent {
		case "claude":
			l.template = settings.TemplateRegistry + "/sandbox-claude-code-docker:latest"
		case "shell":
			l.template = settings.TemplateRegistry + "/sandbox-shell-docker:latest"
		}
	}
	return l, nil
}

// sbxArgs builds the argv after "sbx". The workspace order is the
// launcher's core invariant: worktree first (primary, RW), context mounts
// in configured order, repo last to restore the repo subtree read-write.
func (l *launch) sbxArgs(verb string) []string {
	args := []string{verb, "--name", l.name}
	if !l.exists && l.template != "" {
		args = append(args, "--template", l.template)
	}
	for _, e := range l.env {
		args = append(args, "-e", e)
	}
	args = append(args, l.agent)
	for _, m := range l.mounts {
		args = append(args, m.Arg())
	}
	if len(l.agentArgs) > 0 {
		args = append(args, "--")
		args = append(args, l.agentArgs...)
	}
	return args
}

func (a *app) newRunCmd() *cobra.Command {
	f := &launchFlags{}
	cmd := &cobra.Command{
		Use:   "run [BRANCH [REPO]] [flags] [-- AGENT_ARGS...]",
		Short: "Start or re-attach to a sandboxed agent session on a worktree",
		Long: "Ensures BRANCH has a worktree (creating branch and worktree via wt as\n" +
			"needed), then runs the agent in a sandbox with the worktree read-write,\n" +
			"the configured context mounts, and the main repo read-write (worktrees\n" +
			"share its .git). An existing sandbox is re-attached as-is: workspaces are\n" +
			"fixed at creation, but -e and agent args apply per attach, so\n" +
			"`run BRANCH -- --continue` resumes the previous conversation.\n\n" +
			"BRANCH also accepts a GitHub pull request -- pr:{N} or the PR URL -- which\n" +
			"checks out the PR's head branch into a worktree via wt and names the\n" +
			"sandbox after it. Cross-repo (fork) PRs check out fine but are effectively\n" +
			"read-only from the sandbox: the per-owner GitHub PATs cannot push to a\n" +
			"contributor's fork.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := a.resolveLaunch(f, cmd, args)
			if err != nil {
				return err
			}
			return sbx.ExecAttach(l.sbxArgs("run"))
		},
	}
	a.addLaunchFlags(cmd, f)
	return cmd
}

func (a *app) newCreateCmd() *cobra.Command {
	f := &launchFlags{}
	cmd := &cobra.Command{
		Use:               "create [BRANCH [REPO]] [flags]",
		Short:             "Create a sandbox for a worktree without attaching",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() >= 0 {
				return usagef("create takes no agent args; pass them to run")
			}
			l, err := a.resolveLaunch(f, cmd, args)
			if err != nil {
				return err
			}
			if l.exists {
				fmt.Fprintf(os.Stderr, "sandbox %s already exists\n", l.name)
				return nil
			}
			return sbx.RunInherit(l.sbxArgs("create"))
		},
	}
	a.addLaunchFlags(cmd, f)
	return cmd
}
