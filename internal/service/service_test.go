package service_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/service"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

const prog = `package p
import "sync"
func bad() int {
	var p *int
	return *p
}
func guarded() int {
	var q *int
	if q != nil { return *q }
	return 0
}
func leak() {
	var mu sync.Mutex
	mu.Lock()
}
func ns() {
	var ch chan int
	ch <- 1
}
`

func inputs(src string) []loader.InputFile {
	return []loader.InputFile{{Path: "src/p/a.go", Content: src}}
}

func TestColdStartCoversCategories(t *testing.T) {
	svc := service.New()
	ds, err := svc.Analyze(inputs(prog), service.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := map[diag.Category]bool{}
	for _, d := range ds {
		got[d.Category] = true
	}
	for _, c := range []diag.Category{
		diag.CatNilDeref, diag.CatMutexNotUnlocked, diag.CatNilChanSend,
	} {
		if !got[c] {
			t.Errorf("missing category %s in %+v", c, got)
		}
	}
}

func TestIncrementalEqualsCold(t *testing.T) {
	// 冷启动 prog，再做一系列增量，每次都与全新 service 的冷启动结果对比。
	steps := []string{
		prog,
		prog + "\nfunc extra1() int { return 1 }\n",
		strings.Replace(prog, "return *p", "return 0", 1), // 修复 nil deref
		prog, // 回到原始
		strings.Replace(prog, "ch <- 1", "ch = nil\n\tch <- 1", 1),
	}
	inc := service.New()
	var prev []diag.Diagnostic
	for i, src := range steps {
		got, err := inc.Analyze(inputs(src), service.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		cold, err := service.New().Analyze(inputs(src), service.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, cold) {
			t.Fatalf("step %d: incremental != cold\n inc=%+v\ncold=%+v", i, got, cold)
		}
		prev = got
		_ = prev
	}
}

func TestIncrementalOnlyRecomputesAffected(t *testing.T) {
	svc := service.New()
	if _, err := svc.Analyze(inputs(prog), service.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	// 第二次完全相同输入：不应重算任何函数。
	if _, err := svc.Analyze(inputs(prog), service.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	stats2 := svc.Stats()
	if stats2.Recomputed != 0 {
		t.Fatalf("unchanged input should recompute 0 functions, got %d", stats2.Recomputed)
	}
	if !stats2.HitCache {
		t.Fatal("expected cache hit on unchanged reanalysis")
	}
}

func TestLineSuppression(t *testing.T) {
	src := strings.Replace(prog, "\treturn *p", "\t// nolsa:ignore nil-deref\n\treturn *p", 1)
	svc := service.New()
	ds, _ := svc.Analyze(inputs(src), service.DefaultOptions())
	for _, d := range ds {
		if d.Category == diag.CatNilDeref {
			t.Errorf("suppressed nil-deref must not appear: %+v", d)
		}
	}
}

func TestSuppressionScopedToCategory(t *testing.T) {
	// 压制 nil-deref 不得影响 mutex 缺陷。
	src := strings.Replace(prog, "\treturn *p", "\t// nolsa:ignore nil-deref\n\treturn *p", 1)
	svc := service.New()
	ds, _ := svc.Analyze(inputs(src), service.DefaultOptions())
	var mutex int
	for _, d := range ds {
		if d.Category == diag.CatMutexNotUnlocked {
			mutex++
		}
	}
	if mutex == 0 {
		t.Fatal("suppressing nil-deref must not hide mutex diagnostics")
	}
}

func TestGlobalCategoryDisable(t *testing.T) {
	opts := service.DefaultOptions()
	opts.Suppress.Enabled = map[diag.Category]bool{diag.CatNilChanSend: false}
	svc := service.New()
	ds, _ := svc.Analyze(inputs(prog), opts)
	for _, d := range ds {
		if d.Category == diag.CatNilChanSend {
			t.Errorf("globally disabled category must not appear")
		}
	}
}

func TestDeterministicOrdering(t *testing.T) {
	a, _ := service.New().Analyze(inputs(prog), service.DefaultOptions())
	b, _ := service.New().Analyze(inputs(prog), service.DefaultOptions())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two cold runs produced different output/order")
	}
	for i := 1; i < len(a); i++ {
		x, y := a[i-1].Position, a[i].Position
		if x.File > y.File || (x.File == y.File && (x.Line > y.Line ||
			(x.Line == y.Line && x.Column > y.Column))) {
			t.Fatalf("diagnostics not sorted at %d", i)
		}
	}
}

func TestShowSuppressedKeepsMarked(t *testing.T) {
	src := strings.Replace(prog, "\treturn *p", "\t// nolsa:ignore nil-deref\n\treturn *p", 1)
	opts := service.DefaultOptions()
	opts.ShowSuppressed = true
	ds, _ := service.New().Analyze(inputs(src), opts)
	var saw bool
	for _, d := range ds {
		if d.Category == diag.CatNilDeref {
			saw = true
			if !d.Suppressed {
				t.Errorf("kept diagnostic should be marked Suppressed")
			}
		}
	}
	if !saw {
		t.Fatal("ShowSuppressed should retain suppressed diagnostics")
	}
}

var _ = suppress.DefaultConfig

func TestIncrementalRecomputeScope(t *testing.T) {
	two := "package p\nfunc a() int { return 1 }\nfunc b() int { return a() }\nfunc c() int { return 2 }\n"
	svc := service.New()
	if _, err := svc.Analyze(inputs(two), service.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	// 只改 c()：a/b 不受影响。
	edited := "package p\nfunc a() int { return 1 }\nfunc b() int { return a() }\nfunc c() int { return 3 }\n"
	if _, err := svc.Analyze(inputs(edited), service.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	st := svc.Stats()
	if st.Recomputed == 0 {
		t.Fatal("c changed but nothing recomputed")
	}
	if st.Recomputed > 1 {
		t.Fatalf("only c should be recomputed (no callers), got %d", st.Recomputed)
	}

	// 改 a()：a 及其（传递）调用方 b 都要重算，c 不受影响。
	editedA := "package p\nfunc a() int { return 11 }\nfunc b() int { return a() }\nfunc c() int { return 3 }\n"
	if _, err := svc.Analyze(inputs(editedA), service.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	st = svc.Stats()
	if st.Recomputed != 2 {
		t.Fatalf("a+b should be recomputed (callee change reaches callers), got %d", st.Recomputed)
	}
}

func TestIncrementalPositionsRemainValid(t *testing.T) {
	v1 := "package p\nfunc f() int {\n\tvar p *int\n\treturn *p\n}\n"
	svc := service.New()
	d1, _ := svc.Analyze(inputs(v1), service.DefaultOptions())
	if len(d1) != 1 || d1[0].Position.Line != 4 {
		t.Fatalf("unexpected initial: %+v", d1)
	}
	// 在 f 之前插入两个函数，使 f 的行号整体下移；增量结果必须指向新位置。
	v2 := "package p\nfunc helper1() int { return 1 }\nfunc helper2() int { return 2 }\n" +
		"func f() int {\n\tvar p *int\n\treturn *p\n}\n"
	d2, _ := svc.Analyze(inputs(v2), service.DefaultOptions())
	cold, _ := service.New().Analyze(inputs(v2), service.DefaultOptions())
	if !reflect.DeepEqual(d2, cold) {
		t.Fatalf("incremental positions must match cold after line shift:\n inc=%+v\ncold=%+v", d2, cold)
	}
	if len(d2) != 1 {
		t.Fatalf("expected exactly 1 diag after shift, got %d", len(d2))
	}
	if d2[0].Position.Line != 6 {
		t.Fatalf("deref should now be on line 6, got %d", d2[0].Position.Line)
	}
}

func TestIncrementalDeleteFile(t *testing.T) {
	two := map[string][]loader.InputFile{
		"v1": {
			{Path: "src/p/a.go", Content: "package p\nfunc f() int { var p *int; return *p }\n"},
			{Path: "src/p/b.go", Content: "package p\nfunc g() int { var q *int; return *q }\n"},
		},
	}
	_ = two
	svc := service.New()
	d1, _ := svc.Analyze([]loader.InputFile{
		{Path: "src/p/a.go", Content: "package p\nfunc f() int { var p *int; return *p }\n"},
		{Path: "src/p/b.go", Content: "package p\nfunc g() int { var q *int; return *q }\n"},
	}, service.DefaultOptions())
	if len(d1) != 2 {
		t.Fatalf("expected 2 defects across files, got %d", len(d1))
	}
	// 删除 b.go，只剩 a.go 的缺陷。
	d2, _ := svc.Analyze([]loader.InputFile{
		{Path: "src/p/a.go", Content: "package p\nfunc f() int { var p *int; return *p }\n"},
	}, service.DefaultOptions())
	if len(d2) != 1 || d2[0].Position.File != "src/p/a.go" {
		t.Fatalf("after delete expected only a.go defect, got %+v", d2)
	}
}

func TestIncrementalCallPathRelocation(t *testing.T) {
	v1 := map[string][]loader.InputFile{
		"v1": {
			{Path: "src/p/leaf.go", Content: "package p\nfunc leaf(x *int) int { return *x }\n"},
			{Path: "src/p/caller.go", Content: "package p\nfunc root() int { var p *int; return leaf(p) }\n"},
		},
	}
	_ = v1
	svc := service.New()
	d1, _ := svc.Analyze([]loader.InputFile{
		{Path: "src/p/leaf.go", Content: "package p\nfunc leaf(x *int) int { return *x }\n"},
		{Path: "src/p/caller.go", Content: "package p\nfunc root() int { var p *int; return leaf(p) }\n"},
	}, service.DefaultOptions())
	if len(d1) != 1 || len(d1[0].CallPath) != 2 {
		t.Fatalf("expected 1 diag with 2-node path: %+v", d1)
	}
	before := d1[0].CallPath[0].Position.Line

	// 在 caller.go 顶部插入函数，使 root 调用点下移。
	d2, _ := svc.Analyze([]loader.InputFile{
		{Path: "src/p/leaf.go", Content: "package p\nfunc leaf(x *int) int { return *x }\n"},
		{Path: "src/p/caller.go", Content: "package p\nfunc unused() int { return 7 }\nfunc root() int { var p *int; return leaf(p) }\n"},
	}, service.DefaultOptions())
	cold, _ := service.New().Analyze([]loader.InputFile{
		{Path: "src/p/leaf.go", Content: "package p\nfunc leaf(x *int) int { return *x }\n"},
		{Path: "src/p/caller.go", Content: "package p\nfunc unused() int { return 7 }\nfunc root() int { var p *int; return leaf(p) }\n"},
	}, service.DefaultOptions())
	if !reflect.DeepEqual(d2, cold) {
		t.Fatalf("incremental != cold after caller shift:\n inc=%+v\ncold=%+v", d2, cold)
	}
	if len(d2) != 1 || len(d2[0].CallPath) != 2 {
		t.Fatalf("path lost after shift: %+v", d2)
	}
	after := d2[0].CallPath[0].Position.Line
	if after == before {
		t.Fatalf("call-site line should shift, both=%d", after)
	}
	t.Logf("call site relocated line %d -> %d", before, after)
}

func TestIncrementalMutexPositionRelocation(t *testing.T) {
	v1 := "package p\nimport \"sync\"\nfunc leak() {\n\tvar mu sync.Mutex\n\tmu.Lock()\n}\n"
	svc := service.New()
	d1, _ := svc.Analyze(inputs(v1), service.DefaultOptions())
	if len(d1) != 1 || d1[0].Position.Line != 5 {
		t.Fatalf("initial mutex diag expected line 5: %+v", d1)
	}
	v2 := "package p\nimport \"sync\"\nfunc unused() int { return 1 }\nfunc leak() {\n\tvar mu sync.Mutex\n\tmu.Lock()\n}\n"
	d2, _ := svc.Analyze(inputs(v2), service.DefaultOptions())
	cold, _ := service.New().Analyze(inputs(v2), service.DefaultOptions())
	if !reflect.DeepEqual(d2, cold) {
		t.Fatalf("mutex incremental != cold:\n inc=%+v\ncold=%+v", d2, cold)
	}
	if d2[0].Position.Line != 6 {
		t.Fatalf("mutex Lock line should move to 6, got %d (%+v)", d2[0].Position.Line, d2)
	}
}
