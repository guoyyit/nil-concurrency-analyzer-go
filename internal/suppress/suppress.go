// Package suppress applies inline comment suppressions of the form
//
//	//nca:ignore <category>[,<category>...] [reason]
//
// A directive on the line directly above a diagnostic's position, or at the
// end of the same line, silences the listed categories. Without a category
// list it silences every category at that position.
package suppress

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

const directive = "nca:ignore"

// rule is one parsed suppression directive.
type rule struct {
	file       string
	line       int
	categories map[config.Category]bool // empty means all categories
}

// Suppressor knows which diagnostic sites are silenced by comments.
type Suppressor struct {
	rules []rule
}

// New parses suppression directives from the given packages' comments.
func New(fset *token.FileSet, pkgs map[string]*loader.Package) *Suppressor {
	s := &Suppressor{}
	paths := make([]string, 0, len(pkgs))
	for p := range pkgs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		pkg := pkgs[path]
		for _, f := range pkg.Files {
			for _, cg := range f.Syntax.Comments {
				for _, c := range cg.List {
					s.addComment(fset, c)
				}
			}
		}
	}
	return s
}

func (s *Suppressor) addComment(fset *token.FileSet, c *ast.Comment) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
	if !strings.HasPrefix(text, directive) {
		return
	}
	rest := strings.TrimSpace(text[len(directive):])
	r := rule{categories: map[config.Category]bool{}}
	pos := fset.Position(c.Slash)
	r.file = pos.Filename
	r.line = pos.Line
	if rest != "" {
		// First whitespace-separated token is the category list.
		fields := strings.Fields(rest)
		list := fields[0]
		for _, cat := range strings.Split(list, ",") {
			cat = strings.TrimSpace(cat)
			if cat == "" {
				continue
			}
			r.categories[config.Category(cat)] = true
		}
	}
	s.rules = append(s.rules, r)
}

// IsSuppressed reports whether d is silenced at its position.
func (s *Suppressor) IsSuppressed(d *analysis.Diagnostic) bool {
	for _, r := range s.rules {
		if r.file != d.Pos.File {
			continue
		}
		// Same line or the line directly above.
		if r.line != d.Pos.Line && r.line+1 != d.Pos.Line {
			continue
		}
		if len(r.categories) == 0 || r.categories[d.Category] {
			return true
		}
	}
	return false
}

// Filter removes suppressed diagnostics, preserving order.
func (s *Suppressor) Filter(diags []*analysis.Diagnostic) []*analysis.Diagnostic {
	out := diags[:0]
	for _, d := range diags {
		if s.IsSuppressed(d) {
			continue
		}
		out = append(out, d)
	}
	return out
}
