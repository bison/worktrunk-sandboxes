package sbx

import (
	"fmt"
	"strings"
	"testing"
)

// Recorded fixtures from sbx v0.39.0. The integration suite re-verifies
// these against the live CLI; if an sbx upgrade changes them, both the
// fixtures and the parsers move together.

const ruleAddedFixture = "Rule added to policy local (scope: global): 6d6ccb73-fbd4-45fc-8bbe-4becd9fef99c (wt-sbx-probe.invalid)\n"

func TestParseRuleIDs(t *testing.T) {
	ids := ParseRuleIDs(ruleAddedFixture)
	if len(ids) != 1 || ids[0] != "6d6ccb73-fbd4-45fc-8bbe-4becd9fef99c" {
		t.Fatalf("ids = %v", ids)
	}
	multi := ruleAddedFixture +
		"Rule added to policy local (scope: sandbox my-sb): aaaa-bbbb (api.example.com)\n"
	if ids := ParseRuleIDs(multi); len(ids) != 2 || ids[1] != "aaaa-bbbb" {
		t.Fatalf("multi ids = %v", ids)
	}
	if ids := ParseRuleIDs("nothing here\n"); ids != nil {
		t.Fatalf("no-match ids = %v", ids)
	}
}

func TestParseListText(t *testing.T) {
	out := "NAME              AGENT   STATUS   PORTS   WORKSPACE\n" +
		"myproject-feat      claude  running  -       /Users/x/Code/myorg/myproject/.worktrees/feat\n" +
		"repo-other        shell   stopped  -       /Users/x/Code/org/repo\n"
	sbs := ParseListText(out)
	if len(sbs) != 2 {
		t.Fatalf("got %v", sbs)
	}
	if sbs[0] != (Sandbox{Name: "myproject-feat", Agent: "claude", Status: "running"}) {
		t.Errorf("row 0 = %+v", sbs[0])
	}
	// The empty store prints a notice, not a table.
	if sbs := ParseListText("No sandboxes found.\nLaunch one: sbx run claude\n"); sbs != nil {
		t.Errorf("notice parsed as rows: %v", sbs)
	}
}

func TestListPrefersJSON(t *testing.T) {
	c := NewWithExec(func(args ...string) (string, error) {
		if strings.Join(args, " ") == "ls --json" {
			return `{"sandboxes":[{"name":"a","agent":"claude","status":"running"}]}`, nil
		}
		t.Fatalf("unexpected call %v", args)
		return "", nil
	})
	sbs, err := c.List()
	if err != nil || len(sbs) != 1 || sbs[0].Name != "a" || sbs[0].Status != "running" {
		t.Fatalf("List = %v, %v", sbs, err)
	}
}

func TestListFallsBackToText(t *testing.T) {
	c := NewWithExec(func(args ...string) (string, error) {
		if strings.Join(args, " ") == "ls --json" {
			return "", fmt.Errorf("unknown flag: --json")
		}
		return "NAME  AGENT  STATUS\nx     shell  running\n", nil
	})
	sbs, err := c.List()
	if err != nil || len(sbs) != 1 || sbs[0].Name != "x" {
		t.Fatalf("List = %v, %v", sbs, err)
	}
}

func TestExists(t *testing.T) {
	c := NewWithExec(func(args ...string) (string, error) {
		return "repo-feat\nrepo-feature\n", nil
	})
	if ok, _ := c.Exists("repo-feat"); !ok {
		t.Error("exact name not found")
	}
	if ok, _ := c.Exists("repo-fea"); ok {
		t.Error("prefix matched")
	}
}

func TestPolicyCheckNetwork(t *testing.T) {
	var got []string
	c := NewWithExec(func(args ...string) (string, error) {
		got = args
		return "Allowed: docs.example.com:443\n", nil
	})
	ok, err := c.PolicyCheckNetwork("docs.example.com")
	if err != nil || !ok {
		t.Fatalf("check = %v, %v", ok, err)
	}
	if strings.Join(got, " ") != "policy check network docs.example.com:443" {
		t.Errorf("args = %v", got)
	}
	c = NewWithExec(func(args ...string) (string, error) {
		return "Denied: wt-sbx-probe.invalid:443\nGovernance: Local policy only\n", nil
	})
	if ok, _ := c.PolicyCheckNetwork("wt-sbx-probe.invalid:443"); ok {
		t.Error("denied host reported allowed")
	}
}

func TestWriteGrantsEnvInstallsHook(t *testing.T) {
	var got []string
	c := NewWithExec(func(args ...string) (string, error) {
		got = args
		return "", nil
	})
	if err := c.WriteGrantsEnv("repo-feat", "export A=wt-sbx-a-repo-feat\n"); err != nil {
		t.Fatal(err)
	}
	if len(got) < 5 || strings.Join(got[:5], " ") != "exec repo-feat -- sh -c" {
		t.Fatalf("args = %v", got)
	}
	script := got[len(got)-1]
	if !strings.Contains(script, `> "$HOME/.config/wt-sbx/grants.env"`) {
		t.Errorf("script does not write grants.env: %s", script)
	}
	if !strings.Contains(script, ">> /etc/sandbox-persistent.sh") {
		t.Errorf("script does not install the BASH_ENV hook: %s", script)
	}
	// The append must be guarded so repeated syncs stay idempotent.
	if !strings.Contains(script, "grep -qxF "+shellQuote(grantsEnvHook)) {
		t.Errorf("hook append is not guarded: %s", script)
	}
}

func TestTemplateNames(t *testing.T) {
	c := NewWithExec(func(args ...string) (string, error) {
		return "REPOSITORY                                 TAG                  IMAGE ID       FLAVOR               CREATED\n" +
			"docker.io/docker/sandbox-templates         claude-code-docker   94670d5b2a24   claude-code-docker   10 days ago\n" +
			"quay.io/bison/sandbox-claude-code-docker   latest               a7184b9ae283   claude-code-docker   7 days ago\n", nil
	})
	names := c.TemplateNames()
	want := []string{
		"docker.io/docker/sandbox-templates:claude-code-docker",
		"quay.io/bison/sandbox-claude-code-docker:latest",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("names = %v", names)
	}
}
