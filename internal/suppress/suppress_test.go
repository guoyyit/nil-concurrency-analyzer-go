package suppress

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

func setup(t *testing.T, src string) (*loader.Loader, []*analysis.Diagnostic) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/s\n\ngo 1.25\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644)
	ld, err := loader.New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := ld.LoadAll(); err != nil {
		t.Fatal(err)
	}
	res, err := analysis.New(config.Default()).Analyze(ld, []string{"example.com/s"})
	if err != nil {
		t.Fatal(err)
	}
	var diags []*analysis.Diagnostic
	for _, r := range res {
		diags = append(diags, r.Diagnostics...)
	}
	return ld, diags
}

func TestInlineSuppress(t *testing.T) {
	ld, diags := setup(t, `package main

func get() *int { return nil }

func main() {
	p := get()
	//nca:ignore nil-deref
	_ = *p
	q := get()
	_ = *q
}
`)
	if len(diags) != 2 {
		t.Fatalf("want 2 raw diagnostics, got %d", len(diags))
	}
	s := New(ld.Fset(), ld.Packages())
	kept := s.Filter(diags)
	if len(kept) != 1 {
		t.Fatalf("want 1 diagnostic after suppression, got %d", len(kept))
	}
	if kept[0].Pos.Line != 10 {
		t.Fatalf("wrong diagnostic kept: line %d", kept[0].Pos.Line)
	}
}

func TestSuppressAllCategories(t *testing.T) {
	ld, diags := setup(t, `package main

func get() *int { return nil }

func main() {
	p := get()
	//nca:ignore
	_ = *p
}
`)
	if len(diags) != 1 {
		t.Fatalf("want 1 raw diagnostic, got %d", len(diags))
	}
	s := New(ld.Fset(), ld.Packages())
	if kept := s.Filter(diags); len(kept) != 0 {
		t.Fatalf("want 0 after blanket suppression, got %d", len(kept))
	}
}

func TestSuppressWrongCategory(t *testing.T) {
	ld, diags := setup(t, `package main

func get() *int { return nil }

func main() {
	p := get()
	//nca:ignore copied-lock
	_ = *p
}
`)
	s := New(ld.Fset(), ld.Packages())
	if kept := s.Filter(diags); len(kept) != 1 {
		t.Fatalf("suppression of another category must not hide nil-deref, got %d", len(kept))
	}
}
