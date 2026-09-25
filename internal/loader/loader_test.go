package loader_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

func TestParseAndTypeCheck(t *testing.T) {
	l := loader.New()
	r, err := l.Load([]loader.InputFile{{Path: "src/a.go", Content: `package p
func f(x int) int { return x + 1 }
`}})
	if err != nil {
		t.Fatal(err)
	}
	pi := r.Pkgs["src"]
	if pi == nil || pi.Pkg == nil {
		t.Fatal("package not type-checked")
	}
	if len(pi.Files) != 1 {
		t.Fatalf("files=%d", len(pi.Files))
	}
}

func TestIncrementalReparseOnlyChanged(t *testing.T) {
	l := loader.New()
	src := "package p\nfunc f() int { return 1 }\n"
	r1, _ := l.Load([]loader.InputFile{{Path: "src/a.go", Content: src}})
	if len(r1.Changed.Changed) != 1 {
		t.Fatalf("first load should mark file changed: %v", r1.Changed.Changed)
	}
	r2, _ := l.Load([]loader.InputFile{{Path: "src/a.go", Content: src}})
	if len(r2.Changed.Changed) != 0 {
		t.Fatalf("unchanged reload should have 0 changed files, got %v", r2.Changed.Changed)
	}
	r3, _ := l.Load([]loader.InputFile{{Path: "src/a.go", Content: "package p\nfunc f() int { return 2 }\n"}})
	if len(r3.Changed.Changed) != 1 {
		t.Fatalf("content edit should mark 1 changed, got %v", r3.Changed.Changed)
	}
}

func TestMultiFilePackage(t *testing.T) {
	l := loader.New()
	r, _ := l.Load([]loader.InputFile{
		{Path: "src/a.go", Content: `package p
func f() int { return g() }`},
		{Path: "src/b.go", Content: `package p
func g() int { return 42 }`},
	})
	pi := r.Pkgs["src"]
	if len(pi.Files) != 2 {
		t.Fatalf("expected 2 files grouped, got %d", len(pi.Files))
	}
	if pi.Pkg == nil {
		t.Fatal("cross-file package failed type check")
	}
}
