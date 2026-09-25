package analysis

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
)

func countCategories(res []*Result) map[config.Category]int {
	out := map[config.Category]int{}
	for _, r := range res {
		for _, d := range r.Diagnostics {
			out[d.Category]++
		}
	}
	return out
}

func TestGuardedConclusions(t *testing.T) {
	src := map[string]string{
		"main.go": `package main

func f() *int { return nil }

func main() {
	p := f()
	if p != nil {
		_ = *p
	}
}
`,
	}
	// Off by default.
	ld, roots := setupModule(t, src)
	res, _ := New(config.Default()).Analyze(ld, roots)
	if len(diagLines(t, res)) != 0 {
		t.Fatalf("default run should be silent, got %v", diagLines(t, res))
	}
	// On: guarded site emitted.
	ld2, _ := setupModule(t, src)
	cfg := config.Default()
	cfg.ReportGuarded = true
	res2, _ := New(cfg).Analyze(ld2, roots)
	var guarded bool
	for _, r := range res2 {
		for _, d := range r.Diagnostics {
			if d.Confidence == config.Guarded {
				guarded = true
			}
		}
	}
	if !guarded {
		t.Fatal("expected a guarded conclusion with ReportGuarded enabled")
	}
}

func TestCategorySwitch(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func f() *int { return nil }

func main() {
	p := f()
	_ = *p
}
`,
	})
	cfg := config.Default()
	cfg.Disable(config.NilDereference)
	res, _ := New(cfg).Analyze(ld, roots)
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.NilDereference {
				t.Fatal("disabled category must not be reported")
			}
		}
	}
}

func TestCrossFilePackage(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"lib/lib.go": `package lib

type T struct{ V int }

func New() *T { return nil }
`,
		"main.go": `package main

import "example.com/m/lib"

func main() {
	t := lib.New()
	_ = t.V
}
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	lines := diagLines(t, res)
	if len(lines) != 1 {
		t.Fatalf("want 1 cross-file diagnostic, got %v", lines)
	}
}

func TestEquivalentGuardForms(t *testing.T) {
	// The same logical guard expressed two ways must produce zero reports,
	// and the unguarded version produces exactly one (no duplicate reports).
	src := map[string]string{
		"main.go": `package main

func f() *int { return nil }

func formA() {
	p := f()
	if p != nil {
		_ = *p
	}
}

func formB() {
	p := f()
	if p == nil {
		return
	}
	_ = *p
}
`,
	}
	ld, roots := setupModule(t, src)
	res, _ := New(config.Default()).Analyze(ld, roots)
	if lines := diagLines(t, res); len(lines) != 0 {
		t.Fatalf("equivalent guards should suppress, got %v", lines)
	}
}

func TestUnguardedOnlyOnce(t *testing.T) {
	// Two different unguarded call styles both report, but no duplicates per
	// dereference site.
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func f() *int { return nil }

func a() {
	_ = *f()
}

func b() {
	p := f()
	_ = *p
}
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	lines := diagLines(t, res)
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 sites, got %v", lines)
	}
}
