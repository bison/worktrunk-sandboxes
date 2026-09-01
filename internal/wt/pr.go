package wt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PR is a pull-request reference accepted where run/create expect a
// branch: the pr:{N} shortcut `wt switch` understands, or a GitHub PR
// URL. Resolution shells out to gh, which is not a new dependency:
// worktrunk's own PR checkout runs `gh api` for the same lookup.
type PR struct {
	Owner  string // empty for the pr:{N} form
	Repo   string
	Number int
}

var (
	prShortcut = regexp.MustCompile(`^pr:(\d+)$`)
	prURL      = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)([/?#].*)?$`)
)

// ParsePR recognizes the pr:{N} shortcut and github.com pull-request
// URLs (trailing /files, ?query, #fragment tolerated). Anything else --
// including GitLab mr:{N} -- is not a PR reference.
func ParsePR(s string) (PR, bool) {
	if m := prShortcut.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return PR{Number: n}, true
	}
	if m := prURL.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[3])
		return PR{Owner: m[1], Repo: m[2], Number: n}, true
	}
	return PR{}, false
}

// HeadBranch resolves the PR's head branch name, which names the local
// branch `wt switch` will create and therefore the sandbox.
func (p PR) HeadBranch(repoDir string) (string, error) {
	origin, _ := gitOutput(repoDir, "remote", "get-url", "origin")
	path, err := prAPIPath(p, strings.TrimSpace(origin))
	if err != nil {
		return "", err
	}
	out, err := ghOutput(repoDir, "api", path, "--jq", ".head.ref")
	if err != nil {
		return "", fmt.Errorf("gh api %s failed (is gh installed and authenticated?): %w", path, err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return "", fmt.Errorf("gh api %s returned no head branch", path)
	}
	return branch, nil
}

// prAPIPath builds the GitHub API path for p. The pr:{N} form takes
// owner/repo from origin; the URL form carries its own, and a mismatch
// with origin means the URL belongs to a different repo than the one the
// worktree would be created in -- an error, not a guess.
func prAPIPath(p PR, originURL string) (string, error) {
	owner, repo, ok := parseOwnerRepo(originURL)
	if p.Owner == "" {
		if !ok {
			return "", fmt.Errorf("cannot resolve pr:%d: origin %q is not a github.com remote", p.Number, originURL)
		}
		return fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, p.Number), nil
	}
	if ok && (owner != p.Owner || repo != p.Repo) {
		return "", fmt.Errorf("PR %s/%s#%d does not match this repo's origin (%s/%s); run from the right repo or pass REPO",
			p.Owner, p.Repo, p.Number, owner, repo)
	}
	return fmt.Sprintf("repos/%s/%s/pulls/%d", p.Owner, p.Repo, p.Number), nil
}

var originForms = []*regexp.Regexp{
	regexp.MustCompile(`^git@github\.com:([^/]+)/(.+?)(\.git)?$`),
	regexp.MustCompile(`^ssh://git@github\.com/([^/]+)/(.+?)(\.git)?$`),
	regexp.MustCompile(`^https://github\.com/([^/]+)/(.+?)(\.git)?$`),
}

// parseOwnerRepo extracts owner/repo from the common github.com remote
// URL shapes; ok=false for anything else (other hosts, local paths).
func parseOwnerRepo(url string) (owner, repo string, ok bool) {
	for _, re := range originForms {
		if m := re.FindStringSubmatch(url); m != nil {
			return m[1], m[2], true
		}
	}
	return "", "", false
}
