package wt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Mount is one sandbox workspace argument.
type Mount struct {
	Path string
	RO   bool
}

// Arg renders the mount as an sbx workspace argument.
func (m Mount) Arg() string {
	if m.RO {
		return m.Path + ":ro"
	}
	return m.Path
}

var mountVar = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// ExpandMounts expands configured extra-mounts entries: an optional ":ro"
// suffix, a leading "~", and the template variables {{ repo }},
// {{ repo_parent }}, {{ worktree }}, {{ branch }}.
func ExpandMounts(entries []string, repo, worktree, branch string) ([]Mount, error) {
	vars := map[string]string{
		"repo":        repo,
		"repo_parent": filepath.Dir(repo),
		"worktree":    worktree,
		"branch":      branch,
	}
	var mounts []Mount
	for _, entry := range entries {
		m := Mount{Path: entry}
		if strings.HasSuffix(m.Path, ":ro") {
			m.RO = true
			m.Path = strings.TrimSuffix(m.Path, ":ro")
		}
		var badVar string
		m.Path = mountVar.ReplaceAllStringFunc(m.Path, func(s string) string {
			name := mountVar.FindStringSubmatch(s)[1]
			v, ok := vars[name]
			if !ok {
				badVar = name
			}
			return v
		})
		if badVar != "" {
			return nil, fmt.Errorf("extra-mounts %q: unknown variable {{ %s }}", entry, badVar)
		}
		if m.Path == "~" || strings.HasPrefix(m.Path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			m.Path = filepath.Join(home, strings.TrimPrefix(m.Path[1:], "/"))
		}
		if !filepath.IsAbs(m.Path) {
			return nil, fmt.Errorf("extra-mounts %q: not an absolute path after expansion (%s)", entry, m.Path)
		}
		m.Path = filepath.Clean(m.Path)
		mounts = append(mounts, m)
	}
	return mounts, nil
}

// LaunchMounts assembles the full workspace list for a session and enforces
// the launcher's core invariant. sbx mounts workspaces in argument order,
// later mounts shadow earlier ones, and the primary workspace must be RW
// and first. So: worktree first, context mounts in configured order, repo
// last -- a :ro context mount containing the repo buries the primary
// worktree mount, and the trailing repo mount restores the repo subtree
// (.worktrees included) read-write.
func LaunchMounts(extra []Mount, repo, worktree string) ([]Mount, error) {
	repo = filepath.Clean(repo)
	worktree = filepath.Clean(worktree)
	// The trailing-repo repair only works because worktrunk's
	// worktree-path template puts worktrees inside the repo. This is the
	// one hard layout requirement wt-sbx keeps.
	if !contains(repo, worktree) {
		return nil, fmt.Errorf(
			"worktree %s is not inside repo %s; wt-sbx requires worktrunk's worktree-path template to place worktrees in the repo (e.g. .worktrees/{branch})",
			worktree, repo)
	}
	for _, m := range extra {
		// A RW context mount containing the repo or worktree would
		// shadow the primary mount with no repair step behind it.
		if !m.RO && (contains(m.Path, repo) || contains(m.Path, worktree)) {
			return nil, fmt.Errorf(
				"extra-mounts %s: a read-write mount overlapping the repo or worktree would shadow the primary mount; add :ro or drop it", m.Path)
		}
	}
	mounts := make([]Mount, 0, len(extra)+2)
	mounts = append(mounts, Mount{Path: worktree})
	mounts = append(mounts, extra...)
	mounts = append(mounts, Mount{Path: repo})
	return mounts, nil
}

// contains reports whether inner is outer itself or a path under it.
func contains(outer, inner string) bool {
	return inner == outer || strings.HasPrefix(inner, outer+string(filepath.Separator))
}
