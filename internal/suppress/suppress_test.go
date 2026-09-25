package suppress_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

func mkEngine(t *testing.T, src string) *suppress.Engine {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	e := suppress.NewEngine(suppress.DefaultConfig())
	e.ParseComments(fset, []*ast.File{f})
	return e
}

func TestLineDirective(t *testing.T) {
	src := `package p
// nolsa:ignore nil-deref
var _ = 1
`
	e := mkEngine(t, src)
	if !e.IsSuppressed("a.go", 3, diag.CatNilDeref) {
		t.Fatal("directive on previous line should suppress line 3")
	}
	if e.IsSuppressed("a.go", 3, diag.CatMutexCopy) {
		t.Fatal("category-scoped directive must not suppress other category")
	}
	if e.IsSuppressed("b.go", 3, diag.CatNilDeref) {
		t.Fatal("suppression must be file-scoped")
	}
}

func TestTrailingDirective(t *testing.T) {
	src := "package p\nvar x = 1 //nolsa:ignore\n"
	e := mkEngine(t, src)
	if !e.IsSuppressed("a.go", 2, diag.CatNilChanSend) {
		t.Fatal("bare ignore should suppress all categories on same line")
	}
}

func TestApplyFilters(t *testing.T) {
	src := `package p
// nolsa:ignore nil-deref
var _ = 1
`
	e := mkEngine(t, src)
	in := []diag.Diagnostic{
		{Category: diag.CatNilDeref, Position: diag.Position{File: "a.go", Line: 3}},
		{Category: diag.CatMutexCopy, Position: diag.Position{File: "a.go", Line: 3}},
		{Category: diag.CatNilDeref, Position: diag.Position{File: "a.go", Line: 9}},
	}
	out := e.Apply(in, false)
	if len(out) != 2 {
		t.Fatalf("expected 2 remaining, got %d (%+v)", len(out), out)
	}
}

func TestGlobalDisable(t *testing.T) {
	cfg := suppress.Config{Enabled: map[diag.Category]bool{diag.CatNilDeref: false}}
	if cfg.CategoryEnabled(diag.CatNilDeref) {
		t.Fatal("nil-deref should be disabled")
	}
	if !cfg.CategoryEnabled(diag.CatMutexCopy) {
		t.Fatal("other categories stay enabled")
	}
}
