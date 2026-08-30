package wt

import (
	"strings"
	"testing"
)

const (
	repo = "/Users/x/Code/org/repo"
	wtp  = "/Users/x/Code/org/repo/.worktrees/feat"
)

func TestExpandMounts(t *testing.T) {
	mounts, err := ExpandMounts(
		[]string{"{{ repo_parent }}:ro", "/data/{{ branch }}", "{{worktree}}/docs:ro"},
		repo, wtp, "feat")
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		{Path: "/Users/x/Code/org", RO: true},
		{Path: "/data/feat", RO: false},
		{Path: wtp + "/docs", RO: true},
	}
	if len(mounts) != len(want) {
		t.Fatalf("got %v", mounts)
	}
	for i := range want {
		if mounts[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, mounts[i], want[i])
		}
	}
}

func TestExpandMountsErrors(t *testing.T) {
	if _, err := ExpandMounts([]string{"{{ nope }}:ro"}, repo, wtp, "b"); err == nil ||
		!strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown variable not rejected: %v", err)
	}
	if _, err := ExpandMounts([]string{"relative/path"}, repo, wtp, "b"); err == nil {
		t.Error("relative path not rejected")
	}
}

func TestLaunchMountsOrder(t *testing.T) {
	extra := []Mount{{Path: "/Users/x/Code/org", RO: true}}
	mounts, err := LaunchMounts(extra, repo, wtp)
	if err != nil {
		t.Fatal(err)
	}
	// Order is load-bearing: worktree first (primary RW), context mounts,
	// repo last to restore the repo subtree over any :ro shadow.
	got := make([]string, len(mounts))
	for i, m := range mounts {
		got[i] = m.Arg()
	}
	want := []string{wtp, "/Users/x/Code/org:ro", repo}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("mounts = %v, want %v", got, want)
	}
}

func TestLaunchMountsRejectsRWShadow(t *testing.T) {
	// A RW mount containing the repo would shadow the primary mount with
	// no repair step behind it.
	for _, bad := range []string{"/Users/x/Code/org", "/Users/x/Code", repo, wtp} {
		_, err := LaunchMounts([]Mount{{Path: bad}}, repo, wtp)
		if err == nil {
			t.Errorf("RW mount %s not rejected", bad)
		}
	}
	// The same paths :ro are the entire point of the trailing remount.
	for _, ok := range []string{"/Users/x/Code/org", repo} {
		if _, err := LaunchMounts([]Mount{{Path: ok, RO: true}}, repo, wtp); err != nil {
			t.Errorf(":ro mount %s rejected: %v", ok, err)
		}
	}
	// RW mounts not touching repo or worktree are fine.
	if _, err := LaunchMounts([]Mount{{Path: "/data"}}, repo, wtp); err != nil {
		t.Errorf("independent RW mount rejected: %v", err)
	}
}

func TestLaunchMountsRequiresWorktreeInsideRepo(t *testing.T) {
	_, err := LaunchMounts(nil, repo, "/Users/x/worktrees/feat")
	if err == nil || !strings.Contains(err.Error(), "worktree-path") {
		t.Errorf("outside worktree not refused with template pointer: %v", err)
	}
}
