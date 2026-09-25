// Package output renders diagnostics deterministically: results are sorted by
// (file, line, column, category, message) so identical inputs always produce
// byte-identical output.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
)

// Format selects a rendering style.
type Format string

const (
	TextFormat Format = "text"
	JSONFormat Format = "json"
)

// Flatten collects diagnostics from results and sorts them globally.
func Flatten(results []*analysis.Result) []*analysis.Diagnostic {
	var all []*analysis.Diagnostic
	for _, r := range results {
		all = append(all, r.Diagnostics...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Pos.File != b.Pos.File {
			return a.Pos.File < b.Pos.File
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		if a.Pos.Column != b.Pos.Column {
			return a.Pos.Column < b.Pos.Column
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Message < b.Message
	})
	return all
}

// Render writes diagnostics sorted by file/line/column.
func Render(w io.Writer, results []*analysis.Result, format Format) error {
	diags := Flatten(results)
	switch format {
	case JSONFormat:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if diags == nil {
			diags = []*analysis.Diagnostic{}
		}
		return enc.Encode(diags)
	default:
		for _, d := range diags {
			if _, err := fmt.Fprintf(w, "%s [%s/%s] %s\n",
				d.Pos.String(), d.Category, d.Confidence, d.Message); err != nil {
				return err
			}
			for _, cp := range d.CallPath {
				if _, err := fmt.Fprintf(w, "\t<- %s\n", cp.String()); err != nil {
					return err
				}
			}
		}
		return nil
	}
}
