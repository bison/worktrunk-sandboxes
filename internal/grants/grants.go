// Package grants implements the grant engine: time-boxed credentials and
// network access scoped to one sandbox, with revocation driven by undo
// specs recorded at grant time. Network and credentials deliberately share
// one config table, one state file, and one expiry/revocation path.
package grants

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bison/worktrunk-sandboxes/internal/config"
	"github.com/bison/worktrunk-sandboxes/internal/state"
)

// RevokedValue is the garbage written over a live secret when no baseline
// can be resolved -- fail closed.
const RevokedValue = "wt-sbx-revoked"

// SbxOps is the slice of the sbx client the engine needs.
type SbxOps interface {
	SecretSet(service, sandbox, token string) error
	SecretSetSource(service string, src struct{ Ref, Command string }) error
	SecretRm(service, sandbox string) error
	SecretSetCustom(sandbox, host, env, placeholder, value string) error
	SecretRmPlaceholder(placeholder, sandbox string) error
	PolicyAllowNetwork(sandbox string, hosts []string) ([]string, error)
	PolicyRmNetwork(sandbox, id string) error
	PolicyCheckNetwork(host string) (bool, error)
}

// Markers decorates wt's branch list; both calls are best-effort.
type Markers interface {
	Set(repo, branch string) error
	Clear(repo, branch string) error
}

// Engine wires config, state, and sbx together.
type Engine struct {
	Cfg        *config.Config
	Sbx        SbxOps
	Store      *state.Store
	Now        func() time.Time
	RunCommand func(command string) (string, error) // sh -c, for command= sources
	ReadRef    func(ref string) (string, error)     // op read, for ref= sources
	Marker     Markers
	SpawnSweep func(delay time.Duration) // detached `wt-sbx sweep --after`
	Out        io.Writer                 // grant/revoke confirmations
	Log        io.Writer                 // sweep notices
}

// Target names the sandbox a grant applies to.
type Target struct {
	Sandbox string
	Branch  string
	Repo    string
}

// Grant applies the named configured grant to the target for the given
// duration (0 = no expiry).
func (e *Engine) Grant(name string, t Target, dur time.Duration) error {
	if e.Cfg.Missing {
		return fmt.Errorf("no config at %s", e.Cfg.Path)
	}
	g, ok := e.Cfg.Grants[name]
	if !ok {
		known := e.Cfg.GrantNames()
		if len(known) == 0 {
			return fmt.Errorf("no [grants] defined in %s", e.Cfg.Path)
		}
		return fmt.Errorf("unknown grant %q; known grants: %s",
			name, strings.Join(known, ", "))
	}

	var spec string
	switch g.Kind() {
	case "network":
		ids, err := e.Sbx.PolicyAllowNetwork(t.Sandbox, g.Network)
		if err != nil {
			return err
		}
		spec = "netrule:" + strings.Join(ids, ",")
	default:
		val, err := e.resolve(config.SecretSource{Ref: g.Ref, Command: g.Command})
		if err != nil {
			return fmt.Errorf("grant %q: %w", name, err)
		}
		// Resolved here and passed as a static value, deliberately: sbx
		// applies static secret updates to running sandboxes immediately,
		// while stored ref/command sources only bind at sandbox start --
		// and a grant's whole point is taking effect mid-session.
		if g.Kind() == "service" {
			spec = "service:" + g.Service
			if err := e.Sbx.SecretSet(g.Service, t.Sandbox, val); err != nil {
				return err
			}
		} else {
			ph := "wt-sbx-" + name + "-" + t.Sandbox
			spec = "custom:" + g.Host + ":" + g.Env + ":" + ph
			if err := e.Sbx.SecretSetCustom(t.Sandbox, g.Host, g.Env, ph, val); err != nil {
				return err
			}
		}
	}
	return e.finish(name, t, dur, spec)
}

// AllowHost is the ad hoc network approval for one host, outside the
// config -- the answer to an agent reporting a blocked domain.
func (e *Engine) AllowHost(host string, t Target, dur time.Duration) error {
	// Catch a grant name passed by mistake -- it would otherwise add a
	// policy rule for a literal domain like "net-all" matching nothing.
	if _, isGrant := e.Cfg.Grants[host]; isGrant {
		return fmt.Errorf("%q is a grant -- use: wt sbx auth grant %s", host, host)
	}
	ids, err := e.Sbx.PolicyAllowNetwork(t.Sandbox, []string{host})
	if err != nil {
		return err
	}
	return e.finish("allow:"+host, t, dur, "netrule:"+strings.Join(ids, ","))
}

func (e *Engine) finish(name string, t Target, dur time.Duration, spec string) error {
	var expiry int64
	if dur > 0 {
		expiry = e.Now().Add(dur).Unix()
	}
	err := e.Store.Update(func(rows []state.Grant) []state.Grant {
		return append(rows, state.Grant{
			Expiry:  expiry,
			Sandbox: t.Sandbox,
			Branch:  t.Branch,
			Repo:    t.Repo,
			Name:    name,
			Undo:    spec,
		})
	})
	if err != nil {
		return err
	}
	if dur > 0 {
		if e.SpawnSweep != nil {
			e.SpawnSweep(dur)
		}
		fmt.Fprintf(e.Out, "granted %q to %s; revokes in %s\n", name, t.Sandbox, FormatDuration(dur))
	} else {
		fmt.Fprintf(e.Out, "granted %q to %s; no expiry -- revoked on wt merge/remove or manually\n",
			name, t.Sandbox)
	}
	e.syncMarker(t.Repo, t.Branch)
	return nil
}

// Revoke undoes one named grant on the target's sandbox. A missing row is
// an error -- the caller asked for something that is not there.
func (e *Engine) Revoke(name string, t Target) error {
	rows, err := e.Store.Load()
	if err != nil {
		return err
	}
	found := false
	for _, r := range rows {
		if r.Sandbox == t.Sandbox && r.Name == name {
			e.undo(r.Sandbox, r.Undo)
			fmt.Fprintf(e.Out, "revoked %q from %s\n", name, t.Sandbox)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no active grant %q for %s", name, t.Sandbox)
	}
	if err := e.drop(t.Sandbox, name); err != nil {
		return err
	}
	e.syncMarker(t.Repo, t.Branch)
	return nil
}

// RevokeAll undoes every grant on the target's sandbox. An empty state is
// a quiet success: the worktrunk post-merge/post-remove hooks call this
// unconditionally.
func (e *Engine) RevokeAll(t Target) error {
	rows, err := e.Store.Load()
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Sandbox == t.Sandbox {
			e.undo(r.Sandbox, r.Undo)
			fmt.Fprintf(e.Out, "revoked %q from %s\n", r.Name, t.Sandbox)
		}
	}
	if err := e.drop(t.Sandbox, ""); err != nil {
		return err
	}
	e.syncMarker(t.Repo, t.Branch)
	return nil
}

// Sweep revokes every grant past its deadline. Every invocation sweeps, so
// a reboot-killed timer only delays a revoke until the next command;
// status labels such rows OVERDUE in the meantime.
func (e *Engine) Sweep() error {
	rows, err := e.Store.Load()
	if err != nil {
		return err
	}
	now := e.Now()
	for _, r := range rows {
		if !r.Expired(now) {
			continue
		}
		e.undo(r.Sandbox, r.Undo)
		if err := e.drop(r.Sandbox, r.Name); err != nil {
			return err
		}
		e.syncMarker(r.Repo, r.Branch)
		fmt.Fprintf(e.Log, "wt-sbx: expired grant %q revoked from %s\n", r.Name, r.Sandbox)
	}
	return nil
}

// Init applies the global baseline: the [network] allow delta on top of
// sbx's preset (idempotent via the policy-check oracle), and the
// [default.*] read-only secrets as dynamic sources -- stored, not resolved,
// so sbx's refresh cycle keeps them current.
func (e *Engine) Init() error {
	if e.Cfg.Missing {
		return fmt.Errorf("no config at %s", e.Cfg.Path)
	}
	for _, host := range e.Cfg.NetworkAllow {
		allowed, err := e.Sbx.PolicyCheckNetwork(host)
		if err != nil {
			return err
		}
		if allowed {
			continue
		}
		if _, err := e.Sbx.PolicyAllowNetwork("", []string{host}); err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "global network allow: %s\n", host)
	}

	services := make([]string, 0, len(e.Cfg.Defaults))
	for svc := range e.Cfg.Defaults {
		services = append(services, svc)
	}
	sort.Strings(services)
	for _, svc := range services {
		src := e.Cfg.Defaults[svc]
		err := e.Sbx.SecretSetSource(svc, struct{ Ref, Command string }{src.Ref, src.Command})
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "global %s secret installed (read-only baseline)\n", svc)
	}
	return nil
}

// undo performs the sbx-level removal for one grant, driven by the
// recorded undo spec. Failures are tolerated throughout: a grant may
// already be gone (double sweep, manual rm, deleted sandbox).
func (e *Engine) undo(sandbox, spec string) {
	kind, rest, _ := strings.Cut(spec, ":")
	switch kind {
	case "service":
		// Removing a secret does not reach a running sandbox; static
		// value updates do. Overwrite live with the read-only baseline
		// (garbage if resolution fails -- fail closed), then remove the
		// row so a restarted sandbox falls back to the global baseline.
		base := RevokedValue
		if src, ok := e.Cfg.Defaults[rest]; ok {
			if v, err := e.resolve(src); err == nil {
				base = v
			}
		}
		_ = e.Sbx.SecretSet(rest, sandbox, base)
		_ = e.Sbx.SecretRm(rest, sandbox)
	case "custom":
		host, env, ph, ok := ParseCustomSpec(spec)
		if !ok {
			return
		}
		// Live kill first (updates propagate, removal does not), then
		// delete by placeholder (sbx >= 0.39; on older sbx the parked
		// dead value is the whole revocation).
		_ = e.Sbx.SecretSetCustom(sandbox, host, env, ph, RevokedValue)
		_ = e.Sbx.SecretRmPlaceholder(ph, sandbox)
	case "netrule":
		// Policy rules, unlike secrets, propagate both directions
		// immediately -- no downgrade dance.
		for _, id := range strings.Split(rest, ",") {
			if id != "" {
				_ = e.Sbx.PolicyRmNetwork(sandbox, id)
			}
		}
	}
}

// ParseCustomSpec splits "custom:HOST:ENV:PLACEHOLDER". HOST may itself
// contain colons (port suffixes), so the spec is parsed from the right --
// ENV and PLACEHOLDER never contain one.
func ParseCustomSpec(spec string) (host, env, placeholder string, ok bool) {
	rest, found := strings.CutPrefix(spec, "custom:")
	if !found {
		return "", "", "", false
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", "", false
	}
	placeholder = rest[i+1:]
	rest = rest[:i]
	i = strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", "", false
	}
	return rest[:i], rest[i+1:], placeholder, rest[:i] != "" && rest[i+1:] != "" && placeholder != ""
}

func (e *Engine) resolve(src config.SecretSource) (string, error) {
	var val string
	var err error
	switch {
	case src.Ref != "":
		val, err = e.ReadRef(src.Ref)
	case src.Command != "":
		val, err = e.RunCommand(src.Command)
	default:
		return "", fmt.Errorf("needs ref or command")
	}
	if err != nil {
		return "", err
	}
	val = strings.TrimRight(val, "\n")
	if val == "" {
		return "", fmt.Errorf("resolved to an empty secret")
	}
	return val, nil
}

// drop removes rows for sandbox, all of them when name is empty.
func (e *Engine) drop(sandbox, name string) error {
	return e.Store.Update(func(rows []state.Grant) []state.Grant {
		kept := rows[:0]
		for _, r := range rows {
			if r.Sandbox == sandbox && (name == "" || r.Name == name) {
				continue
			}
			kept = append(kept, r)
		}
		return kept
	})
}

// syncMarker sets the wt list marker while any grant is active for the
// branch and clears it when none remain. Best-effort decoration.
func (e *Engine) syncMarker(repo, branch string) {
	if e.Marker == nil || branch == "" {
		return
	}
	rows, err := e.Store.Load()
	if err != nil {
		return
	}
	active := false
	for _, r := range rows {
		if r.Branch == branch && (r.Repo == "" || repo == "" || r.Repo == repo) {
			active = true
			break
		}
	}
	if active {
		_ = e.Marker.Set(repo, branch)
	} else {
		_ = e.Marker.Clear(repo, branch)
	}
}
