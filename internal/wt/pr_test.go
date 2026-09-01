package wt

import (
	"strings"
	"testing"
)

func TestParsePR(t *testing.T) {
	cases := []struct {
		in   string
		want PR
		ok   bool
	}{
		{"pr:7306", PR{Number: 7306}, true},
		{"https://github.com/myorg/myproject/pull/7306", PR{"myorg", "myproject", 7306}, true},
		{"https://github.com/myorg/myproject/pull/7306/files", PR{"myorg", "myproject", 7306}, true},
		{"https://github.com/myorg/myproject/pull/7306#discussion_r1", PR{"myorg", "myproject", 7306}, true},
		{"http://github.com/o/r/pull/1?w=1", PR{"o", "r", 1}, true},
		{"feature/pr:handling", PR{}, false},
		{"pr:abc", PR{}, false},
		{"mr:5", PR{}, false},
		{"https://github.com/o/r/issues/5", PR{}, false},
		{"https://gitlab.com/o/r/-/merge_requests/5", PR{}, false},
		{"main", PR{}, false},
	}
	for _, c := range cases {
		got, ok := ParsePR(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParsePR(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := []struct {
		url, owner, repo string
		ok               bool
	}{
		{"git@github.com:myorg/myproject.git", "myorg", "myproject", true},
		{"git@github.com:myorg/myproject", "myorg", "myproject", true},
		{"ssh://git@github.com/o/r.git", "o", "r", true},
		{"https://github.com/o/r.git", "o", "r", true},
		{"https://github.com/o/r", "o", "r", true},
		{"https://codeberg.org/o/r.git", "", "", false},
		{"/local/path/repo", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseOwnerRepo(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("parseOwnerRepo(%q) = %q, %q, %v; want %q, %q, %v",
				c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestPRAPIPath(t *testing.T) {
	origin := "git@github.com:myorg/myproject.git"

	if p, err := prAPIPath(PR{Number: 9}, origin); err != nil || p != "repos/myorg/myproject/pulls/9" {
		t.Errorf("shortcut: %q, %v", p, err)
	}
	if _, err := prAPIPath(PR{Number: 9}, "https://codeberg.org/o/r"); err == nil {
		t.Error("shortcut with non-github origin: want error")
	}
	if p, err := prAPIPath(PR{"myorg", "myproject", 9}, origin); err != nil || p != "repos/myorg/myproject/pulls/9" {
		t.Errorf("matching URL: %q, %v", p, err)
	}
	// A URL for a different repo than origin must error, not create a
	// worktree in the wrong repo.
	if _, err := prAPIPath(PR{"other", "repo", 9}, origin); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Errorf("mismatched URL: err = %v", err)
	}
	// URL form with an unparseable origin proceeds on the URL's say-so.
	if p, err := prAPIPath(PR{"o", "r", 9}, "/local/clone"); err != nil || p != "repos/o/r/pulls/9" {
		t.Errorf("URL with local origin: %q, %v", p, err)
	}
}
