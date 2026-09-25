package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/l\n\ngo 1.25\n"), 0o644)
	for rel, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(src), 0o644)
	}
	return dir
}

func TestDiscoverAndCheck(t *testing.T) {
	dir := fixture(t, map[string]string{
		"main.go":  "package main\n\nimport \"example.com/l/sub\"\n\nfunc main(){ _ = sub.S() }\n",
		"sub/s.go": "package sub\nfunc S() int { return 1 }\n",
	})
	l, err := New(dir, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := l.ModulePath(); got != "example.com/l" {
		t.Fatalf("module path = %q", got)
	}
	if err := l.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if len(l.Packages()) != 2 {
		t.Fatalf("want 2 packages, got %d", len(l.Packages()))
	}
	deps := l.Dependencies("example.com/l")
	if len(deps) != 1 || deps[0] != "example.com/l/sub" {
		t.Fatalf("main deps = %v", deps)
	}
}

func TestSkipTestdataAndTests(t *testing.T) {
	dir := fixture(t, map[string]string{
		"main.go":       "package main\nfunc main(){}\n",
		"main_test.go":  "package main\nfunc TestX(){}\n",
		"testdata/x.go": "package x\nfunc X(){}\n",
		"_hidden/h.go":  "package h\nfunc H(){}\n",
	})
	l, _ := New(dir, config.Default())
	if err := l.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if len(l.Packages()) != 1 {
		t.Fatalf("want 1 package, got %v", l.Packages())
	}
}

func TestInvalidateAndRecheck(t *testing.T) {
	dir := fixture(t, map[string]string{
		"main.go": "package main\nfunc main(){}\n",
	})
	l, _ := New(dir, config.Default())
	if err := l.LoadAll(); err != nil {
		t.Fatal(err)
	}
	mainAbs := filepath.Join(dir, "main.go")
	aff := l.Invalidate([]string{mainAbs})
	if !aff["example.com/l"] {
		t.Fatalf("main package should be affected: %v", aff)
	}
	if l.Checked("example.com/l") != nil {
		t.Fatal("checked cache should be dropped")
	}
	if err := l.EnsureChecked([]string{"example.com/l"}); err != nil {
		t.Fatal(err)
	}
	if l.Checked("example.com/l") == nil {
		t.Fatal("package should be re-checked")
	}
}
