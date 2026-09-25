package analysis

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
)

func TestCopiedLockParam(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

type Counter struct {
	mu sync.Mutex
	n  int
}

func inc(c Counter) { // copies mutex
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func main() {
	c := Counter{}
	inc(c)
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.CopiedLock {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected copied-lock diagnostic")
	}
}

func TestUnlockPath(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

var mu sync.Mutex

func bad(cond bool) {
	mu.Lock()
	if cond {
		return // leaks lock
	}
	mu.Unlock()
}

func good(cond bool) {
	mu.Lock()
	defer mu.Unlock()
	if cond {
		return
	}
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.UnlockPath {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("want 1 unlock-path diagnostic, got %d", count)
	}
}

func TestNilChanSend(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func bad() {
	var ch chan int
	ch <- 1
}

func good() {
	ch := make(chan int)
	go func() { ch <- 1 }()
	<-ch
}
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.NilChanSend {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("want 1 nil-chan-send diagnostic, got %d", count)
	}
}

func TestRWMutexCopiedByRange(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

type Item struct {
	mu sync.RWMutex
}

func sum(items []Item) {
	for _, it := range items {
		it.mu.RLock()
		it.mu.RUnlock()
	}
}

func main() { sum(nil) }
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	var found bool
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.CopiedLock {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected copied-lock diagnostic for range value")
	}
}

func TestPointerLockNotCopied(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

func use(mu *sync.Mutex) {
	mu.Lock()
	mu.Unlock()
}

func main() {
	var mu sync.Mutex
	use(&mu)
}
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.CopiedLock {
				t.Fatalf("pointer mutex must not be flagged: %v", d)
			}
		}
	}
}

func TestNilChanClose(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

func main() {
	var ch chan int
	close(ch)
}
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	var n int
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.NilChanSend {
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("want 1 nil-chan diagnostic for close, got %d", n)
	}
}

func TestUnlockBothBranches(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

func f(mu *sync.Mutex, c bool) {
	mu.Lock()
	if c {
		mu.Unlock()
		return
	}
	mu.Unlock()
}

func main() {
	var mu sync.Mutex
	f(&mu, true)
}
`,
	})
	res, _ := New(config.Default()).Analyze(ld, roots)
	for _, r := range res {
		for _, d := range r.Diagnostics {
			if d.Category == config.UnlockPath {
				t.Fatalf("lock released on both branches, got: %v", d.Message)
			}
		}
	}
}

func TestIfInitAssignmentNoCrash(t *testing.T) {
	ld, roots := setupModule(t, map[string]string{
		"main.go": `package main

import "sync"

func f() {
	if mu := (&sync.Mutex{}); mu != nil {
		mu.Lock()
		mu.Unlock()
	}
}

func main() { f() }
`,
	})
	res, err := New(config.Default()).Analyze(ld, roots)
	if err != nil {
		t.Fatal(err)
	}
	// Must not panic; count of diagnostics is irrelevant here.
	_ = res
}
