// Package config loads and validates ~/.config/wt-sbx/config.toml.
//
// The schema deliberately mirrors the wt-auth shell tool's config closely
// enough that hand-porting an existing file takes minutes. One extension:
// [settings] accepts path-scoped sub-tables, gitconfig-includeIf style,
// keyed by a directory or repo path. v1 allows only extra-mounts in scoped
// tables; the syntax is generic so more keys can become scopable later
// without a new shape.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Settings is the [settings] table after scoped-override resolution.
type Settings struct {
	TemplateRegistry string
	DefaultModel     string
	Shell            string
	CodeRoots        []string
	ExtraMounts      []string
}

// Scope is one path-scoped [settings."<path>"] override table.
type Scope struct {
	Raw         string // the key as written
	Path        string // resolved: ~-expanded, absolute, symlinks resolved
	ExtraMounts []string
}

// SecretSource is a [default.<service>] entry: where the global read-only
// baseline for a service comes from.
type SecretSource struct {
	Ref     string `toml:"ref"`
	Command string `toml:"command"`
}

// Grant is a [grants.<name>] entry. Exactly one kind is set: Service,
// Host+Env, or Network.
type Grant struct {
	Service string   `toml:"service"`
	Host    string   `toml:"host"`
	Env     string   `toml:"env"`
	Ref     string   `toml:"ref"`
	Command string   `toml:"command"`
	Network []string `toml:"network"`
}

// Kind returns "service", "custom", or "network".
func (g Grant) Kind() string {
	switch {
	case len(g.Network) > 0:
		return "network"
	case g.Service != "":
		return "service"
	default:
		return "custom"
	}
}

// Config is the loaded configuration file.
type Config struct {
	Path         string
	Missing      bool // no file at Path; defaults in effect
	Settings     Settings
	Scopes       []Scope
	NetworkAllow []string
	Defaults     map[string]SecretSource
	Grants       map[string]Grant
}

// DefaultPath returns the config file location, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "wt-sbx", "config.toml")
}

// Load reads and validates the config at path. A missing file is not an
// error: defaults are returned with Missing set, and commands that need
// config entries (grant, init) report the path themselves.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Path:     path,
		Defaults: map[string]SecretSource{},
		Grants:   map[string]Grant{},
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg.Missing = true
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.parse(string(data)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) parse(data string) error {
	var raw struct {
		Settings map[string]toml.Primitive `toml:"settings"`
		Network  struct {
			Allow []string `toml:"allow"`
		} `toml:"network"`
		Default map[string]SecretSource `toml:"default"`
		Grants  map[string]Grant        `toml:"grants"`
	}
	md, err := toml.Decode(data, &raw)
	if err != nil {
		return err
	}

	if err := c.parseSettings(md, raw.Settings); err != nil {
		return err
	}
	c.NetworkAllow = raw.Network.Allow
	if raw.Default != nil {
		c.Defaults = raw.Default
	}
	if raw.Grants != nil {
		c.Grants = raw.Grants
	}

	for name, src := range c.Defaults {
		if src.Ref == "" && src.Command == "" {
			return fmt.Errorf("default.%s: needs ref or command", name)
		}
	}
	for name, g := range c.Grants {
		if err := validateGrant(name, g); err != nil {
			return err
		}
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return fmt.Errorf("unknown key %q", undec[0].String())
	}
	return nil
}

func (c *Config) parseSettings(md toml.MetaData, raw map[string]toml.Primitive) error {
	for key, prim := range raw {
		var err error
		switch key {
		case "template-registry":
			err = md.PrimitiveDecode(prim, &c.Settings.TemplateRegistry)
		case "default-model":
			err = md.PrimitiveDecode(prim, &c.Settings.DefaultModel)
		case "shell":
			err = md.PrimitiveDecode(prim, &c.Settings.Shell)
		case "code-roots":
			err = md.PrimitiveDecode(prim, &c.Settings.CodeRoots)
		case "extra-mounts":
			err = md.PrimitiveDecode(prim, &c.Settings.ExtraMounts)
		default:
			scope, serr := parseScope(md, key, prim)
			if serr != nil {
				return serr
			}
			c.Scopes = append(c.Scopes, scope)
		}
		if err != nil {
			return fmt.Errorf("settings.%s: %w", key, err)
		}
	}
	// Longest path first so SettingsFor's first match is the winner:
	// exact repo path beats directory prefix beats global.
	sort.Slice(c.Scopes, func(i, j int) bool {
		return len(c.Scopes[i].Path) > len(c.Scopes[j].Path)
	})
	return nil
}

func parseScope(md toml.MetaData, key string, prim toml.Primitive) (Scope, error) {
	var table map[string]toml.Primitive
	if md.Type("settings", key) != "Hash" {
		return Scope{}, fmt.Errorf("settings: unknown key %q", key)
	}
	if err := md.PrimitiveDecode(prim, &table); err != nil {
		return Scope{}, fmt.Errorf("settings.%q: %w", key, err)
	}
	scope := Scope{Raw: key, Path: ResolvePath(key)}
	for k, p := range table {
		if k != "extra-mounts" {
			return Scope{}, fmt.Errorf("settings.%q: key %q is not scopable yet (only extra-mounts)", key, k)
		}
		if err := md.PrimitiveDecode(p, &scope.ExtraMounts); err != nil {
			return Scope{}, fmt.Errorf("settings.%q.extra-mounts: %w", key, err)
		}
		if scope.ExtraMounts == nil {
			scope.ExtraMounts = []string{}
		}
	}
	if scope.ExtraMounts == nil {
		return Scope{}, fmt.Errorf("settings.%q: scoped table needs extra-mounts", key)
	}
	return scope, nil
}

func validateGrant(name string, g Grant) error {
	kinds := 0
	if len(g.Network) > 0 {
		kinds++
	}
	if g.Service != "" {
		kinds++
	}
	if g.Host != "" || g.Env != "" {
		kinds++
	}
	if kinds > 1 {
		return fmt.Errorf("grants.%s: service, host+env, and network are mutually exclusive", name)
	}
	switch {
	case len(g.Network) > 0:
		if g.Ref != "" || g.Command != "" {
			return fmt.Errorf("grants.%s: network grants take no ref or command", name)
		}
	case g.Service != "":
		if g.Ref == "" && g.Command == "" {
			return fmt.Errorf("grants.%s: missing ref or command", name)
		}
	case g.Host != "" && g.Env != "":
		if g.Ref == "" && g.Command == "" {
			return fmt.Errorf("grants.%s: missing ref or command", name)
		}
	case g.Host != "":
		return fmt.Errorf("grants.%s: missing env", name)
	case g.Env != "":
		return fmt.Errorf("grants.%s: missing host", name)
	default:
		return fmt.Errorf("grants.%s: needs service, host+env, or network", name)
	}
	return nil
}

// GrantNames returns the configured grant names, sorted.
func (c *Config) GrantNames() []string {
	names := make([]string, 0, len(c.Grants))
	for n := range c.Grants {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SettingsFor resolves the effective settings for a repo. The match key is
// the main-repo path (never a worktree or cwd); the longest matching scope
// wins per key, and an override replaces the global value -- never merges,
// so "no context mounts for this repo" stays expressible.
func (c *Config) SettingsFor(repoPath string) Settings {
	s := c.Settings
	repo := ResolvePath(repoPath)
	for _, scope := range c.Scopes { // sorted longest path first
		if scope.Path == repo || strings.HasPrefix(repo, scope.Path+string(filepath.Separator)) {
			s.ExtraMounts = scope.ExtraMounts
			break
		}
	}
	return s
}

// ResolvePath normalizes a path for scope matching: ~-expanded, absolute,
// symlinks resolved where possible, trailing-slash insensitive.
func ResolvePath(p string) string {
	if p == "~" {
		home, _ := os.UserHomeDir()
		p = home
	} else if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(resolveSymlinks(filepath.Clean(p)))
}

// resolveSymlinks resolves as much of p as exists, so a path under a
// symlinked ancestor (macOS /var -> /private/var) matches whether or not
// the leaf exists yet.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveSymlinks(parent), filepath.Base(p))
}
