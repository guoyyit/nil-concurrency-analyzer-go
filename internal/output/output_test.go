package output

import (
	"bytes"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

func mkDiag(file string, line, col int, cat config.Category, msg string) *analysis.Diagnostic {
	return &analysis.Diagnostic{
		Category:   cat,
		Confidence: config.Certain,
		Pos:        loader.FilePos{File: file, Line: line, Column: col},
		Message:    msg,
	}
}

func TestDeterministicSort(t *testing.T) {
	// Two results with interleaved diagnostics; rendering must be identical
	// regardless of input order.
	d1 := mkDiag("b.go", 3, 2, config.NilDereference, "x")
	d2 := mkDiag("a.go", 9, 1, config.CopiedLock, "y")
	d3 := mkDiag("a.go", 2, 5, config.NilChanSend, "z")
	d4 := mkDiag("a.go", 2, 5, config.CopiedLock, "w")

	r1 := []*analysis.Result{
		{Package: "p1", Diagnostics: []*analysis.Diagnostic{d1, d2}},
		{Package: "p2", Diagnostics: []*analysis.Diagnostic{d3, d4}},
	}
	r2 := []*analysis.Result{
		{Package: "p2", Diagnostics: []*analysis.Diagnostic{d4, d3}},
		{Package: "p1", Diagnostics: []*analysis.Diagnostic{d2, d1}},
	}

	var b1, b2 bytes.Buffer
	if err := Render(&b1, r1, TextFormat); err != nil {
		t.Fatal(err)
	}
	if err := Render(&b2, r2, TextFormat); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Fatalf("non-deterministic output:\n%s\nvs\n%s", b1.String(), b2.String())
	}
	want := "a.go:2:5 [copied-lock/certain] w\n" +
		"a.go:2:5 [nil-chan-send/certain] z\n" +
		"a.go:9:1 [copied-lock/certain] y\n" +
		"b.go:3:2 [nil-deref/certain] x\n"
	if b1.String() != want {
		t.Fatalf("unexpected order:\n%s", b1.String())
	}
}

func TestJSONDeterministic(t *testing.T) {
	d := mkDiag("a.go", 1, 1, config.NilDereference, "m")
	r := []*analysis.Result{{Package: "p", Diagnostics: []*analysis.Diagnostic{d}}}
	var b1, b2 bytes.Buffer
	Render(&b1, r, JSONFormat)
	Render(&b2, r, JSONFormat)
	if b1.String() != b2.String() {
		t.Fatal("JSON output not deterministic")
	}
}
