package grants

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bison/worktrunk-sandboxes/internal/config"
	"github.com/bison/worktrunk-sandboxes/internal/state"
)

// fakeSbx records every sbx-level operation in order.
type fakeSbx struct {
	calls   []string
	ruleIDs []string
}

func (f *fakeSbx) call(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeSbx) SecretSet(service, sandbox, token string) error {
	f.call("secret-set %s sb=%s val=%s", service, sandbox, token)
	return nil
}

func (f *fakeSbx) SecretSetSource(service string, src struct{ Ref, Command string }) error {
	f.call("secret-set-source %s ref=%s cmd=%s", service, src.Ref, src.Command)
	return nil
}

func (f *fakeSbx) SecretRm(service, sandbox string) error {
	f.call("secret-rm %s sb=%s", service, sandbox)
	return nil
}

func (f *fakeSbx) SecretSetCustom(sandbox, host, env, placeholder, value string) error {
	f.call("secret-set-custom sb=%s host=%s env=%s ph=%s val=%s", sandbox, host, env, placeholder, value)
	return nil
}

func (f *fakeSbx) SecretRmPlaceholder(placeholder, sandbox string) error {
	f.call("secret-rm-ph %s sb=%s", placeholder, sandbox)
	return nil
}

func (f *fakeSbx) PolicyAllowNetwork(sandbox string, hosts []string) ([]string, error) {
	f.call("policy-allow sb=%s hosts=%s", sandbox, strings.Join(hosts, ","))
	if f.ruleIDs == nil {
		return []string{"id-1"}, nil
	}
	return f.ruleIDs, nil
}

func (f *fakeSbx) PolicyRmNetwork(sandbox, id string) error {
	f.call("policy-rm sb=%s id=%s", sandbox, id)
	return nil
}

func (f *fakeSbx) PolicyCheckNetwork(host string) (bool, error) {
	f.call("policy-check %s", host)
	return false, nil
}

type fakeMarkers struct{ calls []string }

func (m *fakeMarkers) Set(repo, branch string) error {
	m.calls = append(m.calls, "set "+branch)
	return nil
}

func (m *fakeMarkers) Clear(repo, branch string) error {
	m.calls = append(m.calls, "clear "+branch)
	return nil
}

type fixture struct {
	eng    *Engine
	sbx    *fakeSbx
	mark   *fakeMarkers
	out    *bytes.Buffer
	log    *bytes.Buffer
	now    time.Time
	sweeps []time.Duration
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg, err := config.Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Missing = false
	cfg.Defaults["github"] = config.SecretSource{Command: "echo ro-baseline"}
	cfg.Grants["gh-write"] = config.Grant{Service: "github", Command: "echo write-token"}
	cfg.Grants["grafana-rw"] = config.Grant{Host: "grafana.example.com:3000", Env: "GRAFANA_TOKEN", Ref: "op://x"}
	cfg.Grants["net-all"] = config.Grant{Network: []string{"**"}}

	f := &fixture{
		sbx:  &fakeSbx{},
		mark: &fakeMarkers{},
		out:  &bytes.Buffer{},
		log:  &bytes.Buffer{},
		now:  time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
	f.eng = &Engine{
		Cfg:   cfg,
		Sbx:   f.sbx,
		Store: state.New(t.TempDir()),
		Now:   func() time.Time { return f.now },
		RunCommand: func(command string) (string, error) {
			return strings.TrimPrefix(command, "echo ") + "\n", nil
		},
		ReadRef:    func(ref string) (string, error) { return "ref-value:" + ref, nil },
		Marker:     f.mark,
		SpawnSweep: func(d time.Duration) { f.sweeps = append(f.sweeps, d) },
		Out:        f.out,
		Log:        f.log,
	}
	return f
}

var target = Target{Sandbox: "repo-feat", Branch: "feat", Repo: "/code/org/repo"}

func (f *fixture) rows(t *testing.T) []state.Grant {
	t.Helper()
	rows, err := f.eng.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestServiceGrantAndRevoke(t *testing.T) {
	f := newFixture(t)
	if err := f.eng.Grant("gh-write", target, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := f.sbx.calls; len(got) != 1 || got[0] != "secret-set github sb=repo-feat val=write-token" {
		t.Fatalf("grant calls = %v", got)
	}
	rows := f.rows(t)
	if len(rows) != 1 || rows[0].Undo != "service:github" ||
		rows[0].Expiry != f.now.Add(time.Hour).Unix() || rows[0].Repo != target.Repo {
		t.Fatalf("state = %+v", rows)
	}
	if len(f.sweeps) != 1 || f.sweeps[0] != time.Hour {
		t.Errorf("sweep timer = %v", f.sweeps)
	}
	if !strings.Contains(f.mark.calls[len(f.mark.calls)-1], "set feat") {
		t.Errorf("marker calls = %v", f.mark.calls)
	}

	f.sbx.calls = nil
	if err := f.eng.Revoke("gh-write", target); err != nil {
		t.Fatal(err)
	}
	// Downgrade THEN remove: the live value must be overwritten with the
	// resolved baseline before the scoped row is removed.
	want := []string{
		"secret-set github sb=repo-feat val=ro-baseline",
		"secret-rm github sb=repo-feat",
	}
	if strings.Join(f.sbx.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("revoke calls = %v", f.sbx.calls)
	}
	if len(f.rows(t)) != 0 {
		t.Error("state row survived revoke")
	}
	if f.mark.calls[len(f.mark.calls)-1] != "clear feat" {
		t.Errorf("marker not cleared: %v", f.mark.calls)
	}
}

func TestServiceRevokeFailsClosed(t *testing.T) {
	f := newFixture(t)
	if err := f.eng.Grant("gh-write", target, 0); err != nil {
		t.Fatal(err)
	}
	// Baseline resolution failing must park garbage, not the old value.
	f.eng.RunCommand = func(string) (string, error) { return "", fmt.Errorf("op offline") }
	f.sbx.calls = nil
	if err := f.eng.Revoke("gh-write", target); err != nil {
		t.Fatal(err)
	}
	if f.sbx.calls[0] != "secret-set github sb=repo-feat val="+RevokedValue {
		t.Errorf("revoke calls = %v", f.sbx.calls)
	}
}

func TestCustomGrantRoundTrip(t *testing.T) {
	f := newFixture(t)
	if err := f.eng.Grant("grafana-rw", target, 0); err != nil {
		t.Fatal(err)
	}
	rows := f.rows(t)
	wantSpec := "custom:grafana.example.com:3000:GRAFANA_TOKEN:wt-sbx-grafana-rw-repo-feat"
	if rows[0].Undo != wantSpec {
		t.Fatalf("undo spec = %q, want %q", rows[0].Undo, wantSpec)
	}
	// The undo spec round-trips even with a colon (port) in the host.
	host, env, ph, ok := ParseCustomSpec(rows[0].Undo)
	if !ok || host != "grafana.example.com:3000" || env != "GRAFANA_TOKEN" ||
		ph != "wt-sbx-grafana-rw-repo-feat" {
		t.Fatalf("ParseCustomSpec = %q %q %q %v", host, env, ph, ok)
	}

	f.sbx.calls = nil
	if err := f.eng.Revoke("grafana-rw", target); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"secret-set-custom sb=repo-feat host=grafana.example.com:3000 env=GRAFANA_TOKEN ph=wt-sbx-grafana-rw-repo-feat val=" + RevokedValue,
		"secret-rm-ph wt-sbx-grafana-rw-repo-feat sb=repo-feat",
	}
	if strings.Join(f.sbx.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("revoke calls = %v", f.sbx.calls)
	}
}

func TestNetworkGrantAndRevoke(t *testing.T) {
	f := newFixture(t)
	f.sbx.ruleIDs = []string{"id-a", "id-b"}
	if err := f.eng.Grant("net-all", target, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.rows(t)[0].Undo != "netrule:id-a,id-b" {
		t.Fatalf("undo = %q", f.rows(t)[0].Undo)
	}
	f.sbx.calls = nil
	if err := f.eng.Revoke("net-all", target); err != nil {
		t.Fatal(err)
	}
	want := []string{"policy-rm sb=repo-feat id=id-a", "policy-rm sb=repo-feat id=id-b"}
	if strings.Join(f.sbx.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("revoke calls = %v", f.sbx.calls)
	}
}

func TestAllowHostGuardrail(t *testing.T) {
	f := newFixture(t)
	err := f.eng.AllowHost("net-all", target, 0)
	if err == nil || !strings.Contains(err.Error(), "auth grant net-all") {
		t.Errorf("grant name as host not rejected: %v", err)
	}
	if err := f.eng.AllowHost("api.example.com", target, 0); err != nil {
		t.Fatal(err)
	}
	if f.rows(t)[0].Name != "allow:api.example.com" {
		t.Errorf("row = %+v", f.rows(t)[0])
	}
}

func TestUnknownGrantListsKnown(t *testing.T) {
	f := newFixture(t)
	err := f.eng.Grant("nope", target, 0)
	if err == nil || !strings.Contains(err.Error(), "gh-write, grafana-rw, net-all") {
		t.Errorf("err = %v", err)
	}
}

func TestRevokeAllQuietOnEmptyState(t *testing.T) {
	f := newFixture(t)
	if err := f.eng.RevokeAll(target); err != nil {
		t.Fatalf("RevokeAll on empty state: %v", err)
	}
	if err := f.eng.Revoke("gh-write", target); err == nil {
		t.Error("named revoke of absent grant did not error")
	}
}

func TestSweepFakeClock(t *testing.T) {
	f := newFixture(t)
	if err := f.eng.Grant("gh-write", target, time.Hour); err != nil {
		t.Fatal(err)
	}
	other := Target{Sandbox: "repo-other", Branch: "other", Repo: target.Repo}
	if err := f.eng.Grant("net-all", other, 0); err != nil {
		t.Fatal(err)
	}

	f.sbx.calls = nil
	f.now = f.now.Add(30 * time.Minute)
	if err := f.eng.Sweep(); err != nil {
		t.Fatal(err)
	}
	if len(f.sbx.calls) != 0 || len(f.rows(t)) != 2 {
		t.Fatalf("early sweep acted: calls=%v rows=%d", f.sbx.calls, len(f.rows(t)))
	}

	f.now = f.now.Add(31 * time.Minute)
	if err := f.eng.Sweep(); err != nil {
		t.Fatal(err)
	}
	rows := f.rows(t)
	if len(rows) != 1 || rows[0].Name != "net-all" {
		t.Fatalf("post-sweep rows = %+v", rows)
	}
	if !strings.Contains(f.log.String(), `expired grant "gh-write" revoked from repo-feat`) {
		t.Errorf("sweep log = %q", f.log.String())
	}
	if f.sbx.calls[0] != "secret-set github sb=repo-feat val=ro-baseline" {
		t.Errorf("sweep calls = %v", f.sbx.calls)
	}
	// The no-expiry grant on the other branch keeps its marker.
	if f.mark.calls[len(f.mark.calls)-1] != "clear feat" {
		t.Errorf("marker calls = %v", f.mark.calls)
	}
}

func TestInit(t *testing.T) {
	f := newFixture(t)
	f.eng.Cfg.NetworkAllow = []string{"docs.netbird.io"}
	if err := f.eng.Init(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"policy-check docs.netbird.io",
		"policy-allow sb= hosts=docs.netbird.io",
		"secret-set-source github ref= cmd=echo ro-baseline",
	}
	if strings.Join(f.sbx.calls, "|") != strings.Join(want, "|") {
		t.Errorf("init calls = %v", f.sbx.calls)
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"0", 0, false},
		{"90s", 90 * time.Second, false},
		{"45m", 45 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"", 0, true},
		{"nope", 0, true},
		{"-5m", 0, true},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestFormatExpiry(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		expiry int64
		want   string
	}{
		{0, "never"},
		{now.Add(-time.Minute).Unix(), "OVERDUE"},
		{now.Add(12 * time.Minute).Unix(), "12m"},
		{now.Add(90 * time.Minute).Unix(), "1h30m"},
	}
	for _, c := range cases {
		if got := FormatExpiry(c.expiry, now); got != c.want {
			t.Errorf("FormatExpiry(%d) = %q, want %q", c.expiry, got, c.want)
		}
	}
}
