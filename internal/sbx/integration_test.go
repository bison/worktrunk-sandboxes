//go:build integration

package sbx

import (
	"os"
	"testing"
)

// Acceptance tests against the real sbx CLI: each empirical finding the
// parsers depend on becomes a test that fails loudly if an sbx upgrade
// changes the behavior. Runs only with WT_SBX_TEST_SBX=1 (dev machines);
// uses a probe host that matches nothing real and cleans up after itself.

const probeHost = "wt-sbx-integration-probe.invalid"

func guard(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("WT_SBX_TEST_SBX") != "1" {
		t.Skip("set WT_SBX_TEST_SBX=1 to run sbx integration tests")
	}
	return New()
}

func TestIntegrationListJSON(t *testing.T) {
	c := guard(t)
	// The preferred `sbx ls --json` path must exist and parse; every
	// sandbox must at least carry a name.
	out, err := c.execFn("ls", "--json")
	if err != nil {
		t.Fatalf("sbx ls --json no longer exists: %v", err)
	}
	sbs, err := parseListJSON(out)
	if err != nil {
		t.Fatalf("sbx ls --json output changed shape: %v", err)
	}
	for _, sb := range sbs {
		if sb.Name == "" {
			t.Errorf("sandbox with empty name in %q", out)
		}
	}
}

func TestIntegrationNetworkRuleLifecycle(t *testing.T) {
	c := guard(t)
	// Baseline: the probe host must be denied.
	if allowed, err := c.PolicyCheckNetwork(probeHost); err != nil || allowed {
		t.Fatalf("probe host pre-state: allowed=%v err=%v", allowed, err)
	}
	// "Rule added ...: <UUID> (<host>)" -- the UUID is field NF-1.
	ids, err := c.PolicyAllowNetwork("", []string{probeHost})
	if err != nil || len(ids) != 1 {
		t.Fatalf("PolicyAllowNetwork: ids=%v err=%v", ids, err)
	}
	defer func() {
		if err := c.PolicyRmNetwork("", ids[0]); err != nil {
			t.Errorf("cleanup: PolicyRmNetwork: %v", err)
		}
	}()
	// `policy check` is the idempotency oracle: ^Allowed after the rule.
	if allowed, err := c.PolicyCheckNetwork(probeHost); err != nil || !allowed {
		t.Fatalf("probe host after allow: allowed=%v err=%v", allowed, err)
	}
}

func TestIntegrationTemplateLs(t *testing.T) {
	c := guard(t)
	if _, err := c.execFn("template", "ls"); err != nil {
		t.Fatalf("sbx template ls: %v", err)
	}
}
