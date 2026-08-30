// Package printer renders command output: aligned text tables (no box
// drawing) and stable JSON documents. The JSON schema is an API -- see
// docs/json.md -- consumed by the Emacs mode and dashboards; changes must
// be additive only.
package printer

import (
	"encoding/json"
	"io"
	"text/tabwriter"
)

// GrantMarker is the wt list glyph for a branch holding an active grant.
const GrantMarker = "⊝"

// Table writes an aligned table with a header row.
func Table(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	writeRow(tw, header)
	for _, row := range rows {
		writeRow(tw, row)
	}
	tw.Flush()
}

func writeRow(w io.Writer, cells []string) {
	for i, c := range cells {
		if i > 0 {
			io.WriteString(w, "\t")
		}
		io.WriteString(w, c)
	}
	io.WriteString(w, "\n")
}

// JSON writes v as indented JSON with a trailing newline.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
