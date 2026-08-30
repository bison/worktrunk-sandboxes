package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bison/worktrunk-sandboxes/internal/config"
	"github.com/bison/worktrunk-sandboxes/internal/grants"
	"github.com/bison/worktrunk-sandboxes/internal/printer"
	"github.com/bison/worktrunk-sandboxes/internal/sbx"
	"github.com/bison/worktrunk-sandboxes/internal/state"
	"github.com/bison/worktrunk-sandboxes/internal/wt"
)

func (a *app) newListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list [--all]",
		Short: "Show worktrees joined with their sandboxes and grants",
		Long: "Inside a repo, shows that repo's worktrees joined with their sandboxes;\n" +
			"--all (or running outside a repo) widens to every repo under the\n" +
			"configured code-roots. Sandboxes matching no worktree are listed as\n" +
			"orphaned -- dashboards need to see drift, not have it hidden.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runList(cmd, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "scan all code-roots, not just the current repo")
	return cmd
}

// worktreeRow is one worktree in the joined view (docs/json.md).
type worktreeRow struct {
	Repo          string   `json:"repo"`
	Branch        string   `json:"branch"`
	Path          string   `json:"path"`
	Sandbox       *string  `json:"sandbox"`
	SandboxStatus *string  `json:"sandbox_status"`
	Agent         *string  `json:"agent"`
	Grants        []string `json:"grants"`
	Marker        *string  `json:"marker"`
}

// sandboxRow is one sandbox in the joined view (docs/json.md).
type sandboxRow struct {
	Name     string  `json:"name"`
	Agent    string  `json:"agent"`
	Status   string  `json:"status"`
	Repo     *string `json:"repo"`
	Branch   *string `json:"branch"`
	Orphaned bool    `json:"orphaned"`
}

type listDoc struct {
	Worktrees []worktreeRow `json:"worktrees"`
	Sandboxes []sandboxRow  `json:"sandboxes"`
	Grants    []grantJSON   `json:"grants"`
}

func (a *app) runList(cmd *cobra.Command, all bool) error {
	repo, err := wt.MainRepo(".")
	inRepo := err == nil
	global := all || !inRepo

	var repos []string
	if global {
		repos = scanCodeRoots(a.cfg.Settings.CodeRoots)
		if len(repos) == 0 && !inRepo {
			fmt.Fprintf(os.Stderr,
				"wt-sbx: no repos found; set code-roots in %s or run inside a repo\n", a.cfg.Path)
		}
	}
	if inRepo && !containsStr(repos, config.ResolvePath(repo)) {
		repos = append(repos, config.ResolvePath(repo))
	}
	sort.Strings(repos)

	sandboxes, err := a.sbx.List()
	if err != nil {
		return err
	}
	rows, err := a.store.Load()
	if err != nil {
		return err
	}
	doc := buildListDoc(repos, sandboxes, rows, global, time.Now())

	if a.format == "json" {
		return printer.JSON(os.Stdout, doc)
	}
	return renderListText(os.Stdout, doc)
}

// buildListDoc joins the three sources: git worktrees per repo, sbx ls
// (names decode to repo-branch by our naming convention), and the grants
// state file.
func buildListDoc(repos []string, sandboxes []sbx.Sandbox, stateRows []state.Grant, global bool, now time.Time) listDoc {
	byName := map[string]sbx.Sandbox{}
	for _, s := range sandboxes {
		byName[s.Name] = s
	}
	grantsBySandbox := map[string][]state.Grant{}
	for _, r := range stateRows {
		grantsBySandbox[r.Sandbox] = append(grantsBySandbox[r.Sandbox], r)
	}

	doc := listDoc{Worktrees: []worktreeRow{}, Sandboxes: []sandboxRow{}, Grants: []grantJSON{}}
	claimed := map[string]struct{ repo, branch string }{}
	scoped := map[string]bool{} // sandbox names in scope for the grants section

	for _, repo := range repos {
		wts, err := wt.Worktrees(repo)
		if err != nil {
			continue
		}
		sort.Slice(wts, func(i, j int) bool { return wts[i].Branch < wts[j].Branch })
		for _, w := range wts {
			if w.Bare {
				continue
			}
			row := worktreeRow{Repo: repo, Branch: w.Branch, Path: w.Path, Grants: []string{}}
			if w.Branch != "" {
				name := wt.SandboxName(repo, w.Branch)
				scoped[name] = true
				if sb, ok := byName[name]; ok {
					claimed[name] = struct{ repo, branch string }{repo, w.Branch}
					row.Sandbox = &sb.Name
					row.SandboxStatus = strPtrOrNil(sb.Status)
					row.Agent = strPtrOrNil(sb.Agent)
				}
				for _, g := range grantsBySandbox[name] {
					row.Grants = append(row.Grants, g.Name)
				}
				if len(row.Grants) > 0 {
					m := printer.GrantMarker
					row.Marker = &m
				}
			}
			doc.Worktrees = append(doc.Worktrees, row)
		}
	}

	// Sandbox → repo/branch for unclaimed names, best-effort via the
	// grants state; everything still unmatched is orphaned.
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	repoBases := map[string]string{}
	for _, r := range repos {
		repoBases[filepath.Base(r)] = r
	}
	for _, n := range names {
		sb := byName[n]
		row := sandboxRow{Name: sb.Name, Agent: sb.Agent, Status: sb.Status}
		if c, ok := claimed[n]; ok {
			row.Repo, row.Branch = &c.repo, &c.branch
		} else {
			for _, g := range grantsBySandbox[n] {
				if g.Repo != "" {
					row.Repo, row.Branch = strPtrOrNil(g.Repo), strPtrOrNil(g.Branch)
					break
				}
			}
			row.Orphaned = true
			// Repo-scoped view: only surface orphans that look like ours.
			if !global && !belongsToRepos(n, row, repoBases) {
				continue
			}
			scoped[n] = true
		}
		doc.Sandboxes = append(doc.Sandboxes, row)
	}

	for _, r := range stateRows {
		if global || scoped[r.Sandbox] {
			doc.Grants = append(doc.Grants, oneGrantJSON(r, now))
		}
	}
	return doc
}

func belongsToRepos(name string, row sandboxRow, repoBases map[string]string) bool {
	if row.Repo != nil {
		return repoBases[filepath.Base(*row.Repo)] == *row.Repo
	}
	for base := range repoBases {
		if strings.HasPrefix(name, base+"-") {
			return true
		}
	}
	return false
}

func renderListText(w *os.File, doc listDoc) error {
	if len(doc.Worktrees) == 0 && len(doc.Sandboxes) == 0 {
		fmt.Fprintln(w, "no worktrees or sandboxes")
		return nil
	}
	now := time.Now()
	expiry := map[string]string{} // "sandbox\x00name" -> rendered expiry
	for _, g := range doc.Grants {
		e := "never"
		if g.Overdue {
			e = "OVERDUE"
		} else if g.ExpiresAt != nil {
			if t, err := time.Parse(time.RFC3339, *g.ExpiresAt); err == nil {
				e = grants.FormatExpiry(t.Unix(), now)
			}
		}
		expiry[g.Sandbox+"\x00"+g.Name] = e
	}

	header := []string{"BRANCH", "REPO", "SANDBOX", "AGENT", "GRANTS", "MARK"}
	var rows [][]string
	for _, wr := range doc.Worktrees {
		branch := wr.Branch
		if branch == "" {
			branch = "(detached)"
		}
		status := "none"
		if wr.Sandbox != nil {
			status = strOr(wr.SandboxStatus, "exists")
		}
		var gcells []string
		for _, g := range wr.Grants {
			key := strOr(wr.Sandbox, "") + "\x00" + g
			if e, ok := expiry[key]; ok && e != "never" {
				gcells = append(gcells, g+"("+e+")")
			} else {
				gcells = append(gcells, g)
			}
		}
		rows = append(rows, []string{
			branch, filepath.Base(wr.Repo), status,
			strOr(wr.Agent, "-"), dashIfEmpty(strings.Join(gcells, ",")),
			strOr(wr.Marker, ""),
		})
	}
	for _, sr := range doc.Sandboxes {
		if !sr.Orphaned {
			continue
		}
		repo := "-"
		if sr.Repo != nil {
			repo = filepath.Base(*sr.Repo)
		}
		rows = append(rows, []string{
			strOr(sr.Branch, "-"), repo,
			sr.Name + " (orphaned, " + dashIfEmpty(sr.Status) + ")",
			dashIfEmpty(sr.Agent), "-", "",
		})
	}
	printer.Table(w, header, rows)
	return nil
}

// scanCodeRoots finds git repos under the configured roots: any directory
// with a .git entry, up to three levels deep (roots hold org directories
// holding repos). Worktrees are skipped -- they belong to their repo.
func scanCodeRoots(roots []string) []string {
	var repos []string
	seen := map[string]bool{}
	var scan func(dir string, depth int)
	scan = func(dir string, depth int) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if !seen[dir] {
				seen[dir] = true
				repos = append(repos, dir)
			}
			return
		}
		if depth == 0 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			scan(filepath.Join(dir, e.Name()), depth-1)
		}
	}
	for _, root := range roots {
		scan(config.ResolvePath(root), 3)
	}
	return repos
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strOr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
