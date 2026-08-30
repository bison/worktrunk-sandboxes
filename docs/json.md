# wt-sbx JSON output contract

Every read command honors `--format json`. The schemas below are an API:
the Emacs mode and any dashboard consume them, so changes must be
**additive only** -- new keys may appear, existing keys keep their name,
type, and meaning. Consumers must ignore unknown keys.

Conventions:

- All paths are absolute.
- All timestamps are ISO-8601 (RFC 3339) in UTC.
- Nullable fields are explicitly `null`, never omitted, except where
  marked optional.

## `wt-sbx list --format json` (also bare `wt sbx --format json`)

```json
{
  "worktrees": [
    {
      "repo": "/Users/x/Code/myorg/myproject",
      "branch": "my-feature",
      "path": "/Users/x/Code/myorg/myproject/.worktrees/my-feature",
      "sandbox": "myproject-my-feature",
      "sandbox_status": "running",
      "agent": "claude",
      "grants": ["myorg-pr-write"],
      "marker": "⊝"
    }
  ],
  "sandboxes": [
    {
      "name": "myproject-my-feature",
      "agent": "claude",
      "status": "running",
      "repo": "/Users/x/Code/myorg/myproject",
      "branch": "my-feature",
      "orphaned": false
    }
  ],
  "grants": [
    {
      "name": "myorg-pr-write",
      "sandbox": "myproject-my-feature",
      "branch": "my-feature",
      "repo": "/Users/x/Code/myorg/myproject",
      "expires_at": "2026-08-30T13:00:00Z",
      "overdue": false
    }
  ]
}
```

Field notes:

- `worktrees[]`: one entry per git worktree of every repo in scope
  (the current repo, or every repo under `code-roots` with `--all` /
  outside a repo). The main checkout is itself a worktree entry.
  - `branch` is `""` for a detached worktree.
  - `sandbox`, `sandbox_status`, `agent` are `null` when no sandbox
    exists for the worktree's derived sandbox name.
  - `marker` is `"⊝"` while any grant is active for the branch, else
    `null`.
- `sandboxes[]`: every sandbox `sbx ls` reports that is in scope.
  - `orphaned` is `true` when the name matches no known worktree;
    `repo`/`branch` are then best-effort (recovered from grant state)
    or `null`. Orphans are listed, not hidden -- dashboards need to see
    drift.
- `grants[]`: active grant rows from the state file.
  - `expires_at` is `null` for no-expiry grants.
  - `overdue` is `true` when the deadline has passed but the revoke has
    not run yet (e.g. a reboot killed the timer).
  - `repo` is optional (may be absent for rows written by older
    versions).

## `wt-sbx auth status --format json`

```json
{
  "grants": [ ... ]
}
```

Same grant objects as `list`.

## `wt-sbx net log --format json`

```json
{
  "sandbox": "myproject-my-feature",
  "log": ["<raw sbx policy log lines>"]
}
```

`log` is `sbx policy log` output split into lines, verbatim; its inner
format belongs to sbx, not to this contract.

## Exit codes

`0` success, `1` operational failure, `2` usage error. Nothing is ever
read from stdin (all confirmations are flags), so the Emacs mode can run
every command via `make-process`.
