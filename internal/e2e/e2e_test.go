//go:build integration

// Package e2e drives the real wt-sbx binary against the real sbx CLI:
// it creates a sandbox on a scratch git fixture and verifies the
// behaviors the whole design leans on -- the mount invariant, secret and
// policy propagation into a running sandbox, and grant revocation. Runs
// only with WT_SBX_TEST_SBX=1; creates and removes one sandbox named
// after a random fixture repo, and touches no global secrets or rules.
//
// The custom-secret and network subtests talk to httpbin.org through the
// sandbox proxy and skip if it is unreachable.
package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const echoHost = "httpbin.org"

func guard(t *testing.T) {
	t.Helper()
	if os.Getenv("WT_SBX_TEST_SBX") != "1" {
		t.Skip("set WT_SBX_TEST_SBX=1 to run sbx e2e tests")
	}
}

// repoRoot locates the module root from this file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// buildBinary compiles wt-sbx into dir and returns its path.
func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "wt-sbx")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/wt-sbx")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// fixture is the scratch layout: org/<random-repo> with a worktree for
// branch e2e inside it, plus a wt-sbx config and isolated state dir.
type fixture struct {
	bin     string
	org     string
	repo    string
	sandbox string
	config  string
	state   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	base := "wtsbx-e2e-" + hex.EncodeToString(suffix)

	f := &fixture{
		bin:     buildBinary(t, root),
		org:     filepath.Join(root, "org"),
		repo:    filepath.Join(root, "org", base),
		sandbox: base + "-e2e",
		config:  filepath.Join(root, "config.toml"),
		state:   filepath.Join(root, "state"),
	}
	if err := os.MkdirAll(f.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", f.repo,
			"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main",
		}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("commit", "--allow-empty", "-m", "init")
	// The worktree is created with git directly so the test does not
	// depend on the machine's worktrunk config or run its hooks; wt-sbx
	// finds it via `git worktree list` and skips `wt switch`.
	git("worktree", "add", "-b", "e2e", filepath.Join(f.repo, ".worktrees", "e2e"))
	if err := os.WriteFile(filepath.Join(f.repo, ".worktrees", "e2e", "file.txt"),
		[]byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := `[settings]
extra-mounts = ["{{ repo_parent }}:ro"]

[grants.e2e-secret]
host = "` + echoHost + `"
env = "E2E_TOKEN"
command = "echo e2e-real-value"
`
	if err := os.WriteFile(f.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// wtsbx runs the built binary from inside the fixture repo with isolated
// state, returning combined output.
func (f *fixture) wtsbx(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(f.bin, append([]string{"--config", f.config}, args...)...)
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+f.state)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *fixture) mustWtsbx(t *testing.T, args ...string) string {
	t.Helper()
	out, err := f.wtsbx(t, args...)
	if err != nil {
		t.Fatalf("wt-sbx %v: %v\n%s", args, err, out)
	}
	return out
}

// execSh runs a shell script inside the sandbox and returns its output.
// Note exec sessions do not carry the agent session's env (appendix 10),
// so scripts receive everything they need explicitly.
func (f *fixture) execSh(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("sbx", "exec", f.sandbox, "--", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sbx exec: %v\n%s", err, out)
	}
	return string(out)
}

// curlEcho requests the echo service's header-reflection endpoint from
// inside the sandbox, with optional extra curl arguments.
func (f *fixture) curlEcho(t *testing.T, extra string) string {
	t.Helper()
	return f.execSh(t, "curl -sS -m 20 "+extra+" https://"+echoHost+"/headers")
}

func TestEndToEnd(t *testing.T) {
	guard(t)
	f := newFixture(t)

	// wt-sbx create drives the full launcher path: repo/branch resolution,
	// the mount order, and sbx create. The sandbox boots without an attach.
	f.mustWtsbx(t, "create", "e2e", f.repo, "-a", "shell")
	t.Cleanup(func() {
		_, _ = f.wtsbx(t, "auth", "revoke", "--all", "e2e")
		if out, err := exec.Command("sbx", "rm", f.sandbox, "--force").CombinedOutput(); err != nil {
			t.Errorf("cleanup: sbx rm %s: %v\n%s", f.sandbox, err, out)
		}
	})

	t.Run("create is idempotent", func(t *testing.T) {
		out := f.mustWtsbx(t, "create", "e2e", f.repo, "-a", "shell")
		if !strings.Contains(out, "already exists") {
			t.Errorf("second create output: %q", out)
		}
	})

	t.Run("mount invariant", func(t *testing.T) {
		// Appendix 1: mounts apply in argument order, later mounts shadow
		// earlier ones. The org :ro mount buries the worktree; the
		// trailing repo mount must restore worktree and repo read-write.
		wt := filepath.Join(f.repo, ".worktrees", "e2e")
		out := f.execSh(t,
			"cat "+wt+"/file.txt && "+
				"echo w > "+wt+"/probe && echo worktree-rw && "+
				"echo r > "+f.repo+"/probe && echo repo-rw; "+
				"echo o > "+f.org+"/probe 2>/dev/null && echo org-rw || echo org-ro")
		for _, want := range []string{"hello", "worktree-rw", "repo-rw", "org-ro"} {
			if !strings.Contains(out, want) {
				t.Errorf("mount check missing %q in:\n%s", want, out)
			}
		}
		if strings.Contains(out, "org-rw") {
			t.Errorf("org mount is writable:\n%s", out)
		}
	})

	t.Run("egress is default-deny", func(t *testing.T) {
		out := f.curlEcho(t, "")
		if !strings.Contains(out, "Blocked by network policy") {
			t.Fatalf("expected block notice, got:\n%s", out)
		}
	})

	t.Run("net allow reaches the running sandbox", func(t *testing.T) {
		f.mustWtsbx(t, "net", "allow", echoHost, "e2e")
		out := f.curlEcho(t, "")
		if strings.Contains(out, "Blocked by network policy") {
			t.Fatalf("still blocked after net allow:\n%s", out)
		}
		if !strings.Contains(out, "headers") {
			t.Skipf("%s unreachable, skipping proxy subtests:\n%s", echoHost, out)
		}
	})

	placeholder := "wt-sbx-e2e-secret-" + f.sandbox
	t.Run("custom secret grant rewrites in flight", func(t *testing.T) {
		f.mustWtsbx(t, "auth", "grant", "e2e-secret", "-d", "0", "e2e")
		out := f.curlEcho(t, `-H "Authorization: Bearer `+placeholder+`"`)
		if !strings.Contains(out, "Bearer e2e-real-value") {
			t.Fatalf("placeholder not rewritten to secret:\n%s", out)
		}
	})

	t.Run("static overwrite propagates live", func(t *testing.T) {
		// Appendix 3: static value updates reach running sandboxes
		// immediately -- the property revocation's downgrade relies on.
		out, err := exec.Command("sbx", "secret", "set-custom",
			"--sandbox", f.sandbox, "--host", echoHost, "--env", "E2E_TOKEN",
			"--placeholder", placeholder, "--value", "e2e-real-value-2").CombinedOutput()
		if err != nil {
			t.Fatalf("set-custom: %v\n%s", err, out)
		}
		got := f.curlEcho(t, `-H "Authorization: Bearer `+placeholder+`"`)
		if !strings.Contains(got, "Bearer e2e-real-value-2") {
			t.Fatalf("overwritten value not live:\n%s", got)
		}
	})

	t.Run("status lists the grants", func(t *testing.T) {
		out := f.mustWtsbx(t, "auth", "status", "--format", "json")
		var doc struct {
			Grants []struct {
				Name    string `json:"name"`
				Sandbox string `json:"sandbox"`
			} `json:"grants"`
		}
		// mustWtsbx returns combined output; status JSON is stdout-only.
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("status --format json: %v\n%s", err, out)
		}
		names := map[string]bool{}
		for _, g := range doc.Grants {
			if g.Sandbox == f.sandbox {
				names[g.Name] = true
			}
		}
		if !names["e2e-secret"] || !names["allow:"+echoHost] {
			t.Errorf("grants missing from status: %v", names)
		}
	})

	t.Run("revoke --all reaches the running sandbox", func(t *testing.T) {
		f.mustWtsbx(t, "auth", "revoke", "--all", "e2e")

		// The netrule undo must have re-blocked the host (appendix 6:
		// policy rules propagate both directions immediately). This also
		// proves the secret can no longer reach the echo service at all.
		out := f.curlEcho(t, `-H "Authorization: Bearer `+placeholder+`"`)
		if !strings.Contains(out, "Blocked by network policy") {
			t.Fatalf("host still reachable after revoke --all:\n%s", out)
		}
		if strings.Contains(out, "e2e-real-value") {
			t.Fatalf("secret value visible after revoke:\n%s", out)
		}

		status := f.mustWtsbx(t, "auth", "status", "--format", "json")
		if strings.Contains(status, f.sandbox) {
			t.Errorf("grants remain after revoke --all:\n%s", status)
		}
	})
}
