// Package cmd implements the wt-sbx command tree. The binary is dispatched
// by worktrunk git-style, so everything here is reachable as `wt sbx ...`.
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/config"
	"github.com/bison/worktrunk-sandboxes/internal/grants"
	"github.com/bison/worktrunk-sandboxes/internal/printer"
	"github.com/bison/worktrunk-sandboxes/internal/sbx"
	"github.com/bison/worktrunk-sandboxes/internal/state"
	"github.com/bison/worktrunk-sandboxes/internal/wt"
)

// usageError marks a bad invocation: exit code 2 instead of 1.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }

func usagef(format string, a ...any) error {
	return usageError{fmt.Errorf(format, a...)}
}

// app carries the per-invocation wiring shared by all commands.
type app struct {
	configPath string
	format     string
	chdir      string

	cfg   *config.Config
	sbx   *sbx.Client
	store *state.Store
}

// Run executes the CLI and returns the process exit code:
// 0 success, 1 operational failure, 2 usage.
func Run() int {
	a := &app{}
	root := a.newRoot()
	if err := root.Execute(); err != nil {
		var uerr usageError
		if errors.As(err, &uerr) || strings.HasPrefix(err.Error(), "unknown command") ||
			strings.HasPrefix(err.Error(), "unknown flag") ||
			strings.HasPrefix(err.Error(), "unknown shorthand flag") {
			fmt.Fprintf(os.Stderr, "wt-sbx: %s\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "wt-sbx: %s\n", err)
		return 1
	}
	return 0
}

func (a *app) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "wt-sbx",
		Short: "Sandboxed agent sessions on worktrunk-managed worktrees",
		Long: "wt-sbx manages Docker Sandboxes bound to worktrunk-managed git worktrees:\n" +
			"launching agent sessions with the correct mount topology, granting and\n" +
			"revoking time-boxed credentials and network access per sandbox, and showing\n" +
			"worktree+sandbox state. Reachable as `wt sbx` via worktrunk's dispatch.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare `wt sbx` is the zero-argument dashboard: list. Safe only
		// because list is read-only; every mutating verb stays explicit.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q", args[0])
			}
			return a.runList(cmd, false)
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.setup(cmd)
		},
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError{err}
	})

	pf := root.PersistentFlags()
	pf.StringVar(&a.format, "format", "text", "output format: text or json")
	pf.StringVar(&a.configPath, "config", "", "config file (default ~/.config/wt-sbx/config.toml)")
	pf.StringVarP(&a.chdir, "chdir", "C", "", "run as if started in this directory")
	_ = root.RegisterFlagCompletionFunc("format",
		cobra.FixedCompletions([]string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp))

	root.AddCommand(
		a.newListCmd(),
		a.newRunCmd(),
		a.newCreateCmd(),
		a.newEnterCmd(),
		a.newAuthCmd(),
		a.newNetCmd(),
		a.newSweepCmd(),
	)
	return root
}

// setup runs before every command: chdir, config load, client wiring, and
// the opportunistic sweep that keeps expired grants from outliving a
// reboot-killed timer.
func (a *app) setup(cmd *cobra.Command) error {
	if a.chdir != "" {
		if err := os.Chdir(a.chdir); err != nil {
			return err
		}
	}
	if a.format != "text" && a.format != "json" {
		return usagef("--format must be text or json")
	}
	path := a.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		if isCompletion(cmd) {
			cfg = &config.Config{Path: path, Missing: true}
		} else {
			return err
		}
	}
	a.cfg = cfg
	a.sbx = sbx.New()
	a.store = state.New(state.DefaultDir())

	// Every invocation sweeps (cheap when nothing expired: one state-file
	// read). The sweep command itself reports errors; elsewhere a failed
	// sweep must not block the user's actual command.
	if name := cmd.Name(); name != "sweep" && !isCompletion(cmd) && name != "help" {
		_ = a.engine(os.Stderr).Sweep()
	}
	return nil
}

func isCompletion(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "completion" || c.Name() == cobra.ShellCompRequestCmd ||
			c.Name() == cobra.ShellCompNoDescRequestCmd {
			return true
		}
	}
	return false
}

// engine builds the grant engine with real-world wiring.
func (a *app) engine(out io.Writer) *grants.Engine {
	return &grants.Engine{
		Cfg:        a.cfg,
		Sbx:        a.sbx,
		Store:      a.store,
		Now:        time.Now,
		RunCommand: runShellCommand,
		ReadRef:    readOpRef,
		Marker:     wtMarkers{},
		SpawnSweep: spawnDetachedSweep,
		Out:        out,
		Log:        os.Stderr,
	}
}

func runShellCommand(command string) (string, error) {
	out, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		return "", fmt.Errorf("command failed: %s", command)
	}
	return string(out), nil
}

func readOpRef(ref string) (string, error) {
	out, err := exec.Command("op", "read", ref).Output()
	if err != nil {
		return "", fmt.Errorf("op read failed for %s", ref)
	}
	return string(out), nil
}

type wtMarkers struct{}

func (wtMarkers) Set(repo, branch string) error {
	return wt.SetMarker(repo, branch, printer.GrantMarker)
}

func (wtMarkers) Clear(repo, branch string) error {
	return wt.ClearMarker(repo, branch)
}

// target resolves the sandbox a grant command applies to. BRANCH defaults
// to the current branch; the repo is always the main checkout of cwd.
func (a *app) target(branchArg string) (grants.Target, error) {
	repo, err := wt.MainRepo(".")
	if err != nil {
		return grants.Target{}, err
	}
	branch := branchArg
	if branch == "" {
		if branch, err = wt.CurrentBranch("."); err != nil {
			return grants.Target{}, err
		}
	}
	return grants.Target{
		Sandbox: wt.SandboxName(repo, branch),
		Branch:  branch,
		Repo:    repo,
	}, nil
}

// completeBranches completes BRANCH arguments from the resolved repo.
func completeBranches(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	repo, err := wt.MainRepo(".")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return wt.LocalBranches(repo), cobra.ShellCompDirectiveNoFileComp
}
