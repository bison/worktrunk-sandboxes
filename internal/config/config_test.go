package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, data string) (*Config, error) {
	t.Helper()
	cfg := &Config{
		Path:     "test.toml",
		Defaults: map[string]SecretSource{},
		Grants:   map[string]Grant{},
	}
	err := cfg.parse(data)
	return cfg, err
}

func mustParse(t *testing.T, data string) *Config {
	t.Helper()
	cfg, err := parse(t, data)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const example = `
[settings]
template-registry = "quay.io/bison"
default-model = "claude-opus-5"
code-roots = ["~/Code"]
extra-mounts = ["{{ repo_parent }}:ro"]

[network]
allow = ["docs.example.com", "pkgs.example.com"]

[default.github]
command = "op read --account my.1password.eu 'op://sandbox/gh-ro/credential'"

[grants.myorg-pr-write]
service = "github"
command = "op read --account my.1password.eu 'op://sandbox/gh-pr-write/credential'"

[grants.grafana-rw]
host = "grafana.example.com"
env = "GRAFANA_TOKEN"
ref = "op://sandbox/grafana/credential"

[grants.net-all]
network = ["**"]
`

func TestParseExample(t *testing.T) {
	cfg := mustParse(t, example)
	s := cfg.Settings
	if s.TemplateRegistry != "quay.io/bison" || s.DefaultModel != "claude-opus-5" {
		t.Errorf("settings = %+v", s)
	}
	if len(s.ExtraMounts) != 1 || s.ExtraMounts[0] != "{{ repo_parent }}:ro" {
		t.Errorf("extra-mounts = %v", s.ExtraMounts)
	}
	if len(cfg.NetworkAllow) != 2 {
		t.Errorf("network allow = %v", cfg.NetworkAllow)
	}
	if cfg.Defaults["github"].Command == "" {
		t.Error("default.github missing")
	}
	if got := cfg.Grants["myorg-pr-write"].Kind(); got != "service" {
		t.Errorf("pr-write kind = %s", got)
	}
	if got := cfg.Grants["grafana-rw"].Kind(); got != "custom" {
		t.Errorf("grafana kind = %s", got)
	}
	if got := cfg.Grants["net-all"].Kind(); got != "network" {
		t.Errorf("net-all kind = %s", got)
	}
	if names := cfg.GrantNames(); strings.Join(names, ",") != "grafana-rw,myorg-pr-write,net-all" {
		t.Errorf("GrantNames = %v", names)
	}
}

func TestValidationErrorsNameEntryAndKey(t *testing.T) {
	cases := []struct{ toml, wantErr string }{
		{"[grants.x]\nservice = \"github\"\n", "grants.x: missing ref or command"},
		{"[grants.x]\nhost = \"h.example\"\nref = \"op://x\"\n", "grants.x: missing env"},
		{"[grants.x]\nenv = \"TOKEN\"\nref = \"op://x\"\n", "grants.x: missing host"},
		{"[grants.x]\nref = \"op://x\"\n", "grants.x: needs service, host+env, or network"},
		{"[grants.x]\nservice = \"github\"\nnetwork = [\"**\"]\nref = \"op://x\"\n", "mutually exclusive"},
		{"[grants.x]\nnetwork = [\"**\"]\nref = \"op://x\"\n", "network grants take no ref"},
		{"[default.gh]\n", "default.gh: needs ref or command"},
		{"[settings]\nbogus = 1\n", `unknown key "bogus"`},
		{"[bogus]\nx = 1\n", "unknown key"},
	}
	for _, c := range cases {
		_, err := parse(t, c.toml)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("config %q: err = %v, want containing %q", c.toml, err, c.wantErr)
		}
	}
}

func TestScopedSettingsOnlyExtraMounts(t *testing.T) {
	_, err := parse(t, "[settings.\"/x\"]\ndefault-model = \"m\"\n")
	if err == nil || !strings.Contains(err.Error(), "not scopable yet") ||
		!strings.Contains(err.Error(), "default-model") {
		t.Errorf("scoped default-model: err = %v", err)
	}
}

func TestScopedSettingsResolution(t *testing.T) {
	// Real directories so symlink resolution has something to chew on.
	root := t.TempDir()
	code := filepath.Join(root, "Code")
	mono := filepath.Join(code, "oss", "bigmono")
	other := filepath.Join(code, "oss", "small")
	elsewhere := filepath.Join(root, "Else", "repo")
	for _, d := range []string{mono, other, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := mustParse(t, `
[settings]
extra-mounts = ["{{ repo_parent }}:ro"]

[settings."`+code+`"]
extra-mounts = ["/global-code:ro"]

[settings."`+mono+`"]
extra-mounts = []
`)
	// Exact repo path beats directory prefix: bigmono gets the empty
	// override -- replace, not merge, so "no context mounts" stays
	// expressible.
	if got := cfg.SettingsFor(mono).ExtraMounts; len(got) != 0 {
		t.Errorf("bigmono extra-mounts = %v, want []", got)
	}
	// Directory prefix beats global.
	if got := cfg.SettingsFor(other).ExtraMounts; len(got) != 1 || got[0] != "/global-code:ro" {
		t.Errorf("small extra-mounts = %v", got)
	}
	// Outside all scopes: the global value.
	if got := cfg.SettingsFor(elsewhere).ExtraMounts; len(got) != 1 || got[0] != "{{ repo_parent }}:ro" {
		t.Errorf("elsewhere extra-mounts = %v", got)
	}
	// Prefixes are path components: .../oss/bigmono must not match a
	// hypothetical .../oss/bigmono2.
	if got := cfg.SettingsFor(mono + "2").ExtraMounts; len(got) != 1 || got[0] != "/global-code:ro" {
		t.Errorf("bigmono2 extra-mounts = %v", got)
	}
	// Non-mount settings never come from scoped tables.
	if cfg.SettingsFor(mono).DefaultModel != "" {
		t.Error("scoped table leaked non-mount settings")
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Missing {
		t.Error("Missing not set")
	}
}
