package analysis

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

func setupModule(t *testing.T, files map[string]string) (*loader.Loader, []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ld, err := loader.New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := ld.LoadAll(); err != nil {
		t.Fatal(err)
	}
	return ld, []string{"example.com/m"}
}

func diagLines(t *testing.T, res []*Result) []string {
	t.Helper()
	var out []string
	for _, r := range res {
		for _, d := range r.Diagnostics {
			out = append(out, filepath.Base(d.Pos.File)+":"+itoaTest(d.Pos.Line)+":"+itoaTest(d.Pos.Column)+" "+string(d.Category)+" "+string(d.Confidence))
		}
	}
	return out
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

func TestNilDirect(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func f() *int { return nil }

func main() {
	p := f()
	_ = *p
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	lines := diagLines(t, res)
	if len(lines) != 1 {
		t.Fatalf("want 1 diagnostic, got %v", lines)
	}
}

func TestNilGuarded(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func f() *int { return nil }

func main() {
	p := f()
	if p != nil {
		_ = *p
	}
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	if lines := diagLines(t, res); len(lines) != 0 {
		t.Fatalf("want no diagnostic under guard, got %v", lines)
	}
}

func TestCrossFuncErr(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "os"

func load() (*os.File, error) {
	return os.Open("/nope")
}

func good() {
	f, err := load()
	if err != nil {
		return
	}
	_, _ = f.Stat()
}

func bad() {
	f, _ := load()
	_, _ = f.Stat()
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	lines := diagLines(t, res)
	// Exactly one: the f.Stat() in bad() where err was ignored.
	if len(lines) != 1 {
		t.Fatalf("want 1 diagnostic, got %v", lines)
	}
}

func TestBoolIndicator(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

type T struct{ v int }

func lookup() (*T, bool) { return nil, false }

func good() {
	t, ok := lookup()
	if !ok {
		return
	}
	_ = t.v
}

func bad() {
	t, _ := lookup()
	_ = t.v
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	lines := diagLines(t, res)
	if len(lines) != 1 {
		t.Fatalf("want 1 diagnostic, got %v", lines)
	}
}

func TestParamGuardNoFalsePositive(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

type T struct{ v int }

func use(t *T) {
	if t == nil {
		return
	}
	_ = t.v
}

func caller() {
	use(nil)
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	if lines := diagLines(t, res); len(lines) != 0 {
		t.Fatalf("want no diagnostic, got %v", lines)
	}
}

func TestParamNilCertain(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

type T struct{ v int }

func use(t *T) {
	_ = t.v
}

func caller() {
	use(nil)
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	lines := diagLines(t, res)
	if len(lines) != 1 {
		t.Fatalf("want 1 diagnostic, got %v", lines)
	}
}
