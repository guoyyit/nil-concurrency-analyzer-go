package analysis

import "fmt"

// dedupDiags removes identical diagnostics (same category and position) within
// one function while preserving order.
func dedupDiags(ds []*Diagnostic) []*Diagnostic {
	seen := map[string]bool{}
	out := ds[:0]
	for _, d := range ds {
		key := fmt.Sprintf("%s|%s|%d|%d|%s", d.Category, d.Pos.File, d.Pos.Line, d.Pos.Column, d.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}
