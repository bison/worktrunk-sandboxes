package wt

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSandboxName(t *testing.T) {
	cases := []struct{ repo, branch, want string }{
		{"/Users/x/Code/netbirdio/netbird", "my-feature", "netbird-my-feature"},
		{"/Users/x/Code/netbirdio/netbird", "fix/proxy/retry", "netbird-fix-proxy-retry"},
		{"/r", "a", "r-a"},
	}
	for _, c := range cases {
		if got := SandboxName(c.repo, c.branch); got != c.want {
			t.Errorf("SandboxName(%q, %q) = %q, want %q", c.repo, c.branch, got, c.want)
		}
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /Users/x/Code/org/repo\n" +
		"HEAD 1111111111111111111111111111111111111111\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /Users/x/Code/org/repo/.worktrees/my feature\n" +
		"HEAD 2222222222222222222222222222222222222222\n" +
		"branch refs/heads/feat/my-feature\n" +
		"\n" +
		"worktree /Users/x/Code/org/repo/.worktrees/loose\n" +
		"HEAD 3333333333333333333333333333333333333333\n" +
		"detached\n"
	wts := ParseWorktrees(out)
	if len(wts) != 3 {
		t.Fatalf("got %d worktrees, want 3", len(wts))
	}
	if wts[0].Branch != "main" || wts[0].Path != "/Users/x/Code/org/repo" {
		t.Errorf("main worktree parsed as %+v", wts[0])
	}
	// Porcelain survives spaces in paths.
	if wts[1].Path != "/Users/x/Code/org/repo/.worktrees/my feature" {
		t.Errorf("path with space parsed as %q", wts[1].Path)
	}
	if wts[1].Branch != "feat/my-feature" {
		t.Errorf("branch parsed as %q", wts[1].Branch)
	}
	if !wts[2].Detached || wts[2].Branch != "" {
		t.Errorf("detached worktree parsed as %+v", wts[2])
	}
}

// gitFixture builds a real repo with one worktree inside it, the layout
// worktrunk's worktree-path template produces.
func gitFixture(t *testing.T) (repo, worktree string) {
	t.Helper()
	repo = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo,
			"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("commit", "--allow-empty", "-m", "init")
	worktree = filepath.Join(repo, ".worktrees", "feat")
	git("worktree", "add", "-b", "feat", worktree)
	return repo, worktree
}

func TestMainRepoFromInsideWorktree(t *testing.T) {
	repo, worktree := gitFixture(t)
	for _, dir := range []string{repo, worktree} {
		got, err := MainRepo(dir)
		if err != nil {
			t.Fatalf("MainRepo(%s): %v", dir, err)
		}
		if resolve(t, got) != resolve(t, repo) {
			t.Errorf("MainRepo(%s) = %s, want %s", dir, got, repo)
		}
	}
}

func TestCurrentBranchAndWorktreeFor(t *testing.T) {
	repo, worktree := gitFixture(t)
	if b, err := CurrentBranch(worktree); err != nil || b != "feat" {
		t.Errorf("CurrentBranch(worktree) = %q, %v; want feat", b, err)
	}
	w, ok, err := WorktreeFor(repo, "feat")
	if err != nil || !ok {
		t.Fatalf("WorktreeFor: ok=%v err=%v", ok, err)
	}
	if resolve(t, w.Path) != resolve(t, worktree) {
		t.Errorf("WorktreeFor path = %s, want %s", w.Path, worktree)
	}
	if _, ok, _ := WorktreeFor(repo, "nope"); ok {
		t.Error("WorktreeFor(nope) reported a worktree")
	}
	if !BranchExists(repo, "feat") || BranchExists(repo, "nope") {
		t.Error("BranchExists wrong")
	}
}

func resolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
