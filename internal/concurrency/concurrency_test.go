package concurrency_test

import (
	"go/ast"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/concurrency"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/nilflow"
)

func runConc(t *testing.T, src string) []diag.Diagnostic {
	t.Helper()
	l := loader.New()
	r, err := l.Load([]loader.InputFile{{Path: "src/p/a.go", Content: src}})
	if err != nil {
		t.Fatal(err)
	}
	pi := r.Pkgs["src/p"]
	nr := nilflow.NewAnalyzer().Analyze(pi, nil)
	ds := concurrency.NewAnalyzer().Analyze(pi, nr)
	ds = diag.Sort(ds)
	return ds
}

func countCat(ds []diag.Diagnostic, cat diag.Category, line int, conf diag.Confidence) int {
	n := 0
	for _, d := range ds {
		if d.Category == cat && (line == 0 || d.Position.Line == line) &&
			(conf == "" || d.Confidence == conf) {
			n++
		}
	}
	return n
}

func TestMutexCopyValueArg(t *testing.T) {
	src := `package p
import "sync"
func use(mu sync.Mutex) {}
func f() {
	var mu sync.Mutex
	use(mu)
}
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatMutexCopy, 6, diag.ConfidenceHigh) == 0 {
		t.Fatalf("expected high mutex-copy at call site: %+v", ds)
	}
}

func TestMutexCopyEmbedded(t *testing.T) {
	src := `package p
import "sync"
type T struct{ mu sync.Mutex }
func use(t T) {}
func f(t T) { use(t) }
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatMutexCopy, 5, diag.ConfidenceHigh) == 0 {
		t.Fatalf("embedded mutex copy must be detected: %+v", ds)
	}
}

func TestPointerNotCopy(t *testing.T) {
	src := `package p
import "sync"
func use(mu *sync.Mutex) {}
func f(mu *sync.Mutex) { use(mu) }
`
	ds := runConc(t, src)
	if n := countCat(ds, diag.CatMutexCopy, 0, ""); n != 0 {
		t.Fatalf("pointer passing must not be a copy, got %d: %+v", n, ds)
	}
}

func TestMutexUnlockedOnPath(t *testing.T) {
	src := `package p
import "sync"
func f(cond bool) {
	var mu sync.Mutex
	mu.Lock()
	if cond { return }
	mu.Unlock()
}
`
	ds := runConc(t, src)
	// early return 路径未解锁 => high。
	if countCat(ds, diag.CatMutexNotUnlocked, 5, diag.ConfidenceHigh) == 0 {
		t.Fatalf("expected unlock-missing on early-return path: %+v", ds)
	}
}

func TestMutexDeferUnlockClean(t *testing.T) {
	src := `package p
import "sync"
func f() {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
}
`
	ds := runConc(t, src)
	if n := countCat(ds, diag.CatMutexNotUnlocked, 0, ""); n != 0 {
		t.Fatalf("defer unlock must be clean, got %d: %+v", n, ds)
	}
}

func TestUnlockWithoutLockSuspicious(t *testing.T) {
	src := `package p
import "sync"
func f() {
	var mu sync.Mutex
	mu.Unlock()
}
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatMutexNotUnlocked, 5, diag.ConfidenceSuspicious) == 0 {
		t.Fatalf("unlock-without-lock should be suspicious: %+v", ds)
	}
}

func TestDoubleLockHigh(t *testing.T) {
	src := `package p
import "sync"
func f() {
	var mu sync.Mutex
	mu.Lock()
	mu.Lock()
}
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatMutexNotUnlocked, 6, diag.ConfidenceHigh) == 0 {
		t.Fatalf("double lock must be reported high: %+v", ds)
	}
}

func TestNilChannelSendHigh(t *testing.T) {
	src := `package p
func f() {
	var ch chan int
	ch <- 1
}
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatNilChanSend, 4, diag.ConfidenceHigh) == 0 {
		t.Fatalf("nil chan send must be high: %+v", ds)
	}
}

func TestNonNilChannelSendClean(t *testing.T) {
	src := `package p
func f() {
	ch := make(chan int, 1)
	ch <- 1
}
`
	ds := runConc(t, src)
	if n := countCat(ds, diag.CatNilChanSend, 0, ""); n != 0 {
		t.Fatalf("buffered non-nil send must be clean, got %d: %+v", n, ds)
	}
}

func TestMaybeNilChannelSendSuspicious(t *testing.T) {
	src := `package p
func f(cond bool) {
	var ch chan int
	if cond { ch = make(chan int, 1) }
	ch <- 1
}
`
	ds := runConc(t, src)
	if countCat(ds, diag.CatNilChanSend, 5, diag.ConfidenceSuspicious) == 0 {
		t.Fatalf("maybe-nil chan send should be suspicious: %+v", ds)
	}
}

var _ = ast.NewIdent
