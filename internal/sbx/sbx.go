// Package sbx wraps the sbx CLI. There is no public API, so sbx's output
// is a contract: every invocation and every parse lives here, so format
// drift breaks one package. Verified against sbx v0.39.
package sbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

// Client shells out to sbx. The exec function is swappable for tests.
type Client struct {
	execFn func(args ...string) (stdout string, err error)
}

// New returns a Client backed by the real sbx binary.
func New() *Client {
	return &Client{execFn: realExec}
}

// NewWithExec returns a Client with a custom exec function (tests).
func NewWithExec(fn func(args ...string) (string, error)) *Client {
	return &Client{execFn: fn}
}

func realExec(args ...string) (string, error) {
	cmd := exec.Command("sbx", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("sbx %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// Sandbox is one row of `sbx ls`.
type Sandbox struct {
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

// List returns all sandboxes. `sbx ls --json` (v0.39+) is preferred; the
// text table is the fallback for older sbx.
func (c *Client) List() ([]Sandbox, error) {
	out, err := c.execFn("ls", "--json")
	if err == nil {
		return parseListJSON(out)
	}
	out, terr := c.execFn("ls")
	if terr != nil {
		return nil, err
	}
	return ParseListText(out), nil
}

func parseListJSON(out string) ([]Sandbox, error) {
	var doc struct {
		Sandboxes []map[string]any `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("sbx ls --json: %w", err)
	}
	sbs := make([]Sandbox, 0, len(doc.Sandboxes))
	for _, m := range doc.Sandboxes {
		sbs = append(sbs, Sandbox{
			Name:   jsonStr(m, "name"),
			Agent:  jsonStr(m, "agent"),
			Status: jsonStr(m, "status", "state"),
		})
	}
	return sbs, nil
}

func jsonStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		for mk, v := range m {
			if strings.EqualFold(mk, k) {
				if s, ok := v.(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// ParseListText parses the columnar `sbx ls` table: a header row, then one
// sandbox per line with NAME, AGENT, STATUS leading. An empty store prints
// a "No sandboxes found." notice instead of a table.
func ParseListText(out string) []Sandbox {
	var sbs []Sandbox
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "NAME") {
		return nil
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		sb := Sandbox{Name: f[0]}
		if len(f) > 1 {
			sb.Agent = f[1]
		}
		if len(f) > 2 {
			sb.Status = f[2]
		}
		sbs = append(sbs, sb)
	}
	return sbs
}

// Exists reports whether a sandbox with this exact name exists.
func (c *Client) Exists(name string) (bool, error) {
	out, err := c.execFn("ls", "-q")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// SecretSet installs a static per-sandbox (or global, sandbox=="") service
// secret value. Static values propagate to running sandboxes immediately,
// in both set and overwrite directions.
func (c *Client) SecretSet(service, sandbox, token string) error {
	args := []string{"secret", "set", service, "--token", token, "--force"}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	_, err := c.execFn(args...)
	return err
}

// SecretSetSource installs a global dynamic secret source (ref or command).
// Sources bind only at sandbox start, which is exactly right for the global
// baseline: sbx's refresh cycle keeps it current.
func (c *Client) SecretSetSource(service string, src struct{ Ref, Command string }) error {
	args := []string{"secret", "set", service, "--force"}
	switch {
	case src.Ref != "":
		args = append(args, "--ref", src.Ref)
	case src.Command != "":
		args = append(args, "--command", src.Command)
	default:
		return fmt.Errorf("secret source for %s needs ref or command", service)
	}
	_, err := c.execFn(args...)
	return err
}

// SecretRm removes a service secret row. Removal does NOT reach a running
// sandbox -- callers must overwrite the live value first (see grants).
func (c *Client) SecretRm(service, sandbox string) error {
	args := []string{"secret", "rm", service, "--force"}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	_, err := c.execFn(args...)
	return err
}

// SecretSetCustom installs a per-sandbox custom (host+env+placeholder)
// secret with a static value.
func (c *Client) SecretSetCustom(sandbox, host, env, placeholder, value string) error {
	_, err := c.execFn("secret", "set-custom", "--sandbox", sandbox,
		"--host", host, "--env", env, "--placeholder", placeholder,
		"--value", value)
	return err
}

// SecretRmPlaceholder removes a custom secret by placeholder (sbx >= 0.39;
// older sbx cannot address custom secrets, and the overwritten dead value
// is the whole revocation there).
func (c *Client) SecretRmPlaceholder(placeholder, sandbox string) error {
	_, err := c.execFn("secret", "rm", "--placeholder", placeholder,
		"--sandbox", sandbox, "--force")
	return err
}

// SecretListRaw returns `sbx secret ls` output verbatim for display.
func (c *Client) SecretListRaw() (string, error) {
	return c.execFn("secret", "ls")
}

var ruleAdded = regexp.MustCompile(`^Rule added\b`)

// PolicyAllowNetwork adds network allow rules -- per-sandbox when sandbox
// is non-empty, global otherwise -- and returns the rule UUIDs. Policy
// rules propagate to running sandboxes immediately, in both directions.
//
// Output contract (v0.39): one line per rule,
// "Rule added to policy local (scope: ...): <UUID> (<host>)" -- the UUID is
// the second-to-last field.
func (c *Client) PolicyAllowNetwork(sandbox string, hosts []string) ([]string, error) {
	args := []string{"policy", "allow", "network"}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	args = append(args, strings.Join(hosts, ","))
	out, err := c.execFn(args...)
	if err != nil {
		return nil, err
	}
	ids := ParseRuleIDs(out)
	if len(ids) == 0 {
		return nil, fmt.Errorf("sbx policy allow network: no rule ID in output:\n%s", out)
	}
	return ids, nil
}

// ParseRuleIDs extracts rule UUIDs from `sbx policy allow network` output.
func ParseRuleIDs(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if !ruleAdded.MatchString(line) {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 {
			ids = append(ids, f[len(f)-2])
		}
	}
	return ids
}

// PolicyRmNetwork removes one network rule by UUID.
func (c *Client) PolicyRmNetwork(sandbox, id string) error {
	args := []string{"policy", "rm", "network", "--id", id}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	_, err := c.execFn(args...)
	return err
}

// PolicyCheckNetwork reports whether host is currently allowed -- the
// idempotency oracle for the global baseline. A bare hostname is checked
// on :443. sbx exits nonzero for a denied host, so the verdict comes from
// the output ("Allowed"/"Denied" prefix), not the exit status.
func (c *Client) PolicyCheckNetwork(host string) (bool, error) {
	target := host
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	out, err := c.execFn("policy", "check", "network", target)
	switch {
	case strings.HasPrefix(out, "Allowed"):
		return true, nil
	case strings.HasPrefix(out, "Denied"):
		return false, nil
	case err != nil:
		return false, err
	}
	return false, fmt.Errorf("sbx policy check network %s: unrecognized output:\n%s", target, out)
}

// PolicyLogRaw returns `sbx policy log [SANDBOX]` output verbatim.
func (c *Client) PolicyLogRaw(sandbox string) (string, error) {
	args := []string{"policy", "log"}
	if sandbox != "" {
		args = append(args, sandbox)
	}
	return c.execFn(args...)
}

// TemplateNames lists loaded template images as REPOSITORY:TAG (completion).
func (c *Client) TemplateNames() []string {
	out, err := c.execFn("template", "ls")
	if err != nil {
		return nil
	}
	var names []string
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 {
			continue
		}
		names = append(names, f[0]+":"+f[1])
	}
	return names
}

var hostish = regexp.MustCompile(`\b[a-z0-9][a-z0-9.-]*\.[a-z]{2,}\b`)

// RecentBlockedHosts extracts hostnames from blocked entries in the policy
// log, best-effort for completion; empty on any error.
func (c *Client) RecentBlockedHosts(sandbox string) []string {
	out, err := c.PolicyLogRaw(sandbox)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var hosts []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(strings.ToLower(line), "block") &&
			!strings.Contains(strings.ToLower(line), "denied") {
			continue
		}
		for _, h := range hostish.FindAllString(strings.ToLower(line), -1) {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// Agents is sbx's built-in agent list (completion).
func Agents() []string {
	return []string{
		"claude", "codex", "copilot", "cursor", "docker-agent",
		"droid", "gemini", "kiro", "opencode", "shell",
	}
}

// ExecAttach replaces the current process with `sbx args...` -- the attach
// is fully interactive, so exec (not a child process) is the right shape.
func ExecAttach(args []string) error {
	path, err := exec.LookPath("sbx")
	if err != nil {
		return fmt.Errorf("sbx not found")
	}
	return syscall.Exec(path, append([]string{"sbx"}, args...), os.Environ())
}

// RunInherit runs `sbx args...` as a child with inherited stdio (for
// non-interactive operations whose progress the user should see).
func RunInherit(args []string) error {
	cmd := exec.Command("sbx", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
