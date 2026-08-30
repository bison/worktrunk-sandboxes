# worktrunk-sandboxes

`wt-sbx` runs coding agents in [Docker Sandboxes][sandboxes] (`sbx`) on
[worktrunk][worktrunk]-managed git worktrees: it launches sessions with
the correct mount topology, grants and revokes time-boxed credentials and
network access per sandbox, and shows a repo-scoped or global view of
worktree+sandbox state for terminals, dashboards, and editors.

Worktrunk dispatches `wt-<name>` binaries on `PATH` git-style, so the
binary is reachable as `wt sbx <subcommand>`.

[sandboxes]: https://docs.docker.com/ai/sandboxes/
[worktrunk]: https://worktrunk.dev/

## Install

```sh
mage install        # or: go install ./cmd/wt-sbx
```

Requires `git`, `wt`, and `sbx` on `PATH` at runtime.

## Daily workflow

```sh
cd ~/Code/netbirdio/netbird
wt sbx run my-feature                 # create branch+worktree if needed, attach agent
wt sbx run my-feature -- --continue   # re-attach, resume the conversation
wt sbx run my-feature -a shell        # plain shell in the same environment
wt sbx                                # dashboard: worktrees + sandboxes + grants
wt sbx auth grant netbirdio-pr-write  # time-boxed write credential (default 1h)
wt sbx net allow api.example.com      # one blocked host, for the sandbox's life
wt sbx net open -d 30m                # full egress for research (grants net-all)
wt sbx auth revoke --all              # also fires on wt merge/remove via hooks
```

`sbx rm <name>` removes a sandbox; `wt merge`/`wt remove` clean up the
branch and worktree (and, via hooks, its grants).

## Mount topology

For repo `R`, worktree `W = R/.worktrees/<branch>`, and configured
context mounts `E1..En`, a session mounts, in order:

```
W  E1..En  R
```

The order is load-bearing: sbx mounts workspaces in argument order,
later mounts shadow earlier ones, and the primary workspace must be
read-write and first. A `:ro` context mount containing `R` buries the
primary worktree mount; the trailing `R` mount restores the repo
subtree -- `.worktrees` included -- read-write. This works only because
worktrunk's `worktree-path` template puts worktrees **inside** the repo;
that is the one hard layout requirement this tool keeps, and `run`
refuses worktrees living elsewhere.

Note that repo and its worktrees are one isolation unit: worktrees share
the repo's `.git`, so two sandboxes on branches of the same repo can
write each other's worktrees. Sandboxes isolate repos from each other.

## Configuration

`~/.config/wt-sbx/config.toml`:

```toml
[settings]
template-registry = "quay.io/bison"   # -> sandbox-claude-code-docker:latest etc.
default-model = "claude-opus-5"       # sets ANTHROPIC_DEFAULT_MODEL; -m '' disables
code-roots = ["~/Code"]               # where `list --all` scans; not mounts
extra-mounts = ["{{ repo_parent }}:ro"]  # context mounts; built-in default []

[network]
allow = ["docs.netbird.io"]           # global delta on sbx's preset; `auth init`

[default.github]                      # global read-only baseline; `auth init`
command = "op read --account my.1password.eu 'op://sandbox/gh-ro/credential'"

[grants.netbirdio-pr-write]           # service grant: shadows the baseline
service = "github"
command = "op read --account my.1password.eu 'op://sandbox/gh-pr-write/credential'"

[grants.grafana-rw]                   # custom secret: injected by host+env
host = "grafana.example.com"
env = "GRAFANA_TOKEN"
ref = "op://sandbox/grafana/credential"

[grants.net-all]                      # network grant; `net open` sugar
network = ["**"]
```

`extra-mounts` entries take an optional `:ro` suffix and the variables
`{{ repo }}`, `{{ repo_parent }}`, `{{ worktree }}`, `{{ branch }}`.
The built-in default is `[]`: nothing outside the repo is mounted
unconfigured. `{{ repo_parent }}:ro` gives the "sibling repos visible
read-only" layout. Read-write entries that contain the repo or worktree
are rejected -- they would shadow the primary mount with no repair step.

Per-sibling `:ro` enumeration works too (list the siblings explicitly),
but beware `git worktree prune`: worktrees at paths a sandbox cannot see
look `prunable` through the shared `.git` from inside that sandbox.

Settings can be scoped per directory or repo; the longest matching
prefix wins and an override **replaces** the global value (so `[]`
means "no context mounts here"):

```toml
[settings."~/Code"]
extra-mounts = ["{{ repo_parent }}:ro"]

[settings."~/Code/oss/bigmono"]
extra-mounts = ["~/datasets:ro"]
```

Only `extra-mounts` is scopable for now. Repo-local config files are
deliberately unsupported: repo-supplied data must not choose what the
host mounts into an agent sandbox.

## Credentials and network

Grants resolve to static values at grant time (so sbx applies them to
running sandboxes immediately) and are revoked by overwriting the live
value with the read-only baseline -- or garbage, failing closed --
before removing the scoped secret. Network grants add per-sandbox policy
rules, which propagate immediately in both directions. Expiry is a
recorded deadline plus sweeps: every invocation sweeps, a detached timer
sweeps at the deadline, and `auth status` labels rows that outlived a
dead timer `OVERDUE`.

Worktrunk hooks close the lifecycle (in `~/.config/worktrunk/config.toml`):

```toml
[post-merge]
revoke-grants = "wt-sbx auth revoke --all {{ branch }} || true"

[post-remove]
revoke-grants = "wt-sbx auth revoke --all {{ branch }} || true"
```

While a branch holds any grant, `wt list` shows a ⊝ marker on it.

Note that per-sandbox Anthropic accounts are impossible today: sbx's
proxy rewrites anthropic auth wholesale and only the oauth-mode global
secret works. Do not scope an `anthropic` secret to a sandbox.

## Output

Read commands (`list`, `auth status`, `net log`) take `--format json`;
the schema is documented in [docs/json.md](docs/json.md) and treated as
an API. Nothing ever reads stdin.

## Completion

`wt sbx completion zsh` (bash/fish/powershell too) with dynamic
completion for branches, grant names, recently blocked hosts, agents,
and templates. When invoked through `wt`, argument completion depends on
worktrunk delegating to the `wt-sbx` binary; completions always work
when invoked as `wt-sbx` directly.

## Development

```sh
mage build             # -> bin/wt-sbx
mage test:unit         # unit tests
mage lint:go           # golangci-lint
mage test:integration  # integration suite against the real sbx CLI (dev machine only)
```

The integration suite (`WT_SBX_TEST_SBX=1`, build tag `integration`)
re-verifies the sbx output contracts the parsers depend on, so an sbx
upgrade that changes them fails loudly.
