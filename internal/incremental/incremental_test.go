package incremental

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/output"
)

func writeModule(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/inc\n\ngo 1.25\n"), 0o644)
	for rel, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(src), 0o644)
	}
}

func coldRun(t *testing.T, dir string) string {
	t.Helper()
	ld, err := loader.New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := ld.LoadAll(); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range ld.Packages() {
		paths = append(paths, p)
	}
	res, err := analysis.New(config.Default()).Analyze(ld, paths)
	if err != nil {
		t.Fatal(err)
	}
	var buf = &writer{}
	if err := output.Render(buf, res, output.TextFormat); err != nil {
		t.Fatal(err)
	}
	return buf.s
}

type writer struct{ s string }

func (w *writer) Write(p []byte) (int, error) { w.s += string(p); return len(p), nil }

func TestIncrementalMatchesCold(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"main.go": `package main

import "example.com/inc/lib"

func main() {
	p := lib.Get()
	_ = *p
}
`,
		"lib/lib.go": `package lib

func Get() *int { return nil }
`,
	}
	writeModule(t, dir, files)

	eng, err := New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	res1, err := eng.Analyze()
	if err != nil {
		t.Fatal(err)
	}
	var b1 writer
	output.Render(&b1, res1, output.TextFormat)

	cold1 := coldRun(t, dir)
	if b1.s != cold1 {
		t.Fatalf("cold run differs from first incremental run:\n%s\nvs\n%s", b1.s, cold1)
	}

	// Modify lib to introduce a new defect; only lib and main are affected.
	files["lib/lib.go"] = `package lib

func Get() *int { return nil }

func Also() *int { return nil }

func Bad() {
	x := Also()
	_ = *x
}
`
	writeModule(t, dir, files)

	affected, err := eng.AffectedPackages()
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 2 {
		t.Fatalf("want 2 affected packages, got %v", affected)
	}

	res2, err := eng.Analyze()
	if err != nil {
		t.Fatal(err)
	}
	var b2 writer
	output.Render(&b2, res2, output.TextFormat)

	cold2 := coldRun(t, dir)
	if b2.s != cold2 {
		t.Fatalf("incremental result differs from cold run:\nincremental:\n%s\ncold:\n%s", b2.s, cold2)
	}
}

func TestIncrementalNoChange(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, map[string]string{
		"main.go": `package main

func main() {}
`,
	})
	eng, err := New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Analyze(); err != nil {
		t.Fatal(err)
	}
	affected, err := eng.AffectedPackages()
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 0 {
		t.Fatalf("no changes should affect no packages, got %v", affected)
	}
}
