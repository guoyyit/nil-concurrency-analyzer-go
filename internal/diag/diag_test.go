package diag_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

func TestSortAndDedup(t *testing.T) {
	a := diag.Diagnostic{Category: diag.CatNilDeref, Position: diag.Position{File: "b.go", Line: 1}}
	b := diag.Diagnostic{Category: diag.CatNilDeref, Position: diag.Position{File: "a.go", Line: 9, Column: 1}}
	c := diag.Diagnostic{Category: diag.CatNilDeref, Position: diag.Position{File: "a.go", Line: 2}}
	dup := diag.Diagnostic{Category: diag.CatNilDeref, Position: diag.Position{File: "a.go", Line: 2}}
	in := []diag.Diagnostic{a, b, c, dup}
	in = diag.Sort(in)
	if len(in) != 3 {
		t.Fatalf("expected dedup to 3, got %d", len(in))
	}
	if in[0].Position.File != "a.go" || in[0].Position.Line != 2 {
		t.Fatalf("wrong order: %+v", in[0].Position)
	}
	if in[2].Position.File != "b.go" {
		t.Fatalf("b.go should be last")
	}
}

func TestSortStableAcrossRuns(t *testing.T) {
	mk := func() []diag.Diagnostic {
		return []diag.Diagnostic{
			{Category: diag.CatMutexCopy, Position: diag.Position{File: "x.go", Line: 3}, Message: "m"},
			{Category: diag.CatNilDeref, Position: diag.Position{File: "x.go", Line: 1}, Message: "a"},
			{Category: diag.CatNilDeref, Position: diag.Position{File: "x.go", Line: 1, Column: 2}, Message: "b"},
		}
	}
	d1 := mk()
	d2 := mk()
	diag.Sort(d1)
	diag.Sort(d2)
	for i := range d1 {
		if d1[i].Message != d2[i].Message {
			t.Fatal("non-deterministic ordering")
		}
	}
}
