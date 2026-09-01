// Package wt resolves worktrunk/git state: the main repo, sandbox names,
// worktrees, branches, and wt list markers. Worktree lifecycle stays with
// wt itself -- we only call `wt switch [--create]` and read git's porcelain
// output, which is authoritative for worktree paths.
package wt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MainRepo resolves the main checkout for dir, even from inside a worktree:
// --git-common-dir names the shared .git, whose parent is the main repo.
func MainRepo(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("not in a git repository (%s)", dir)
	}
	return filepath.Dir(strings.TrimSpace(out)), nil
}

// SandboxName derives the sandbox name for a repo+branch: the repo basename
// and the branch with slashes turned to dashes, as in wt's sanitize filter.
func SandboxName(repo, branch string) string {
	return filepath.Base(repo) + "-" + strings.ReplaceAll(branch, "/", "-")
}

// CurrentBranch returns the checked-out branch in dir, or an error on
// detached HEAD.
func CurrentBranch(dir string) (string, error) {
	out, err := gitOutput(dir, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("not in a git repository (%s)", dir)
	}
	b := strings.TrimSpace(out)
	if b == "" {
		return "", fmt.Errorf("detached HEAD; name the branch explicitly")
	}
	return b, nil
}

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Branch   string // short name; empty when detached or bare
	Head     string
	Bare     bool
	Detached bool
}

// Worktrees lists the repo's worktrees, main checkout included.
func Worktrees(repo string) ([]Worktree, error) {
	out, err := gitOutput(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list failed in %s", repo)
	}
	return ParseWorktrees(out), nil
}

// ParseWorktrees parses `git worktree list --porcelain` output. Porcelain
// survives spaces in paths, unlike the default columnar output.
func ParseWorktrees(out string) []Worktree {
	var wts []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case line == "bare":
			cur.Bare = true
		case line == "detached":
			cur.Detached = true
		}
	}
	flush()
	return wts
}

// WorktreeFor returns the worktree checked out on branch, if any.
func WorktreeFor(repo, branch string) (Worktree, bool, error) {
	wts, err := Worktrees(repo)
	if err != nil {
		return Worktree{}, false, err
	}
	for _, w := range wts {
		if w.Branch == branch {
			return w, true, nil
		}
	}
	return Worktree{}, false, nil
}

// BranchExists reports whether repo has a local branch by that name.
func BranchExists(repo, branch string) bool {
	err := gitRun(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// LocalBranches lists the repo's local branch names (for completion).
func LocalBranches(repo string) []string {
	out, err := gitOutput(repo, "for-each-ref", "refs/heads", "--format", "%(refname:short)")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// EnsureWorktree makes sure branch has a worktree, creating branch and
// worktree via wt as needed (so worktrunk hooks run), and returns the
// worktree path. Plain `wt switch` materializes a worktree for an existing
// branch; --create is only for branches that do not exist yet. Both print
// a "cannot change directory" notice without shell integration -- harmless,
// the sandbox gets the path explicitly.
func EnsureWorktree(repo, branch string) (string, error) {
	return EnsureWorktreeFrom(repo, branch, "")
}

// EnsureWorktreeFrom is EnsureWorktree for a branch that may need to be
// created from a non-branch source: when branch does not exist and src is
// set (a pr:{N} spec or PR URL), src is what `wt switch` gets -- --create
// would branch off the default branch instead of the PR head.
func EnsureWorktreeFrom(repo, branch, src string) (string, error) {
	w, ok, err := WorktreeFor(repo, branch)
	if err != nil {
		return "", err
	}
	if !ok {
		args := []string{"switch"}
		switch {
		case BranchExists(repo, branch):
			args = append(args, branch)
		case src != "":
			args = append(args, src)
		default:
			args = append(args, "--create", branch)
		}
		cmd := exec.Command("wt", args...)
		cmd.Dir = repo
		cmd.Stdout = os.Stderr // progress, not output
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("wt %s failed: %w", strings.Join(args, " "), err)
		}
		if w, ok, err = WorktreeFor(repo, branch); err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("no worktree found for branch %s", branch)
		}
	}
	return w.Path, nil
}

// SetMarker sets the wt list marker for a branch; ClearMarker removes it.
// Both are best-effort decorations -- errors are ignored by callers.
func SetMarker(repo, branch, marker string) error {
	return wtRun(repo, "config", "state", "marker", "set", "--branch", branch, marker)
}

// ClearMarker removes the wt list marker for a branch.
func ClearMarker(repo, branch string) error {
	return wtRun(repo, "config", "state", "marker", "clear", "--branch", branch)
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

func ghOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func wtRun(dir string, args ...string) error {
	cmd := exec.Command("wt", args...)
	cmd.Dir = dir
	return cmd.Run()
}
