package nilflow_test

import (
	"go/ast"
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/nilflow"
)

// analyzeSrc 在一个包内分析给定源文件集合（支持跨文件）。
func analyzeSrc(t *testing.T, files map[string]string) []diag.Diagnostic {
	t.Helper()
	l := loader.New()
	var in []loader.InputFile
	for p, c := range files {
		in = append(in, loader.InputFile{Path: p, Content: c})
	}
	r, err := l.Load(in)
	if err != nil {
		t.Fatal(err)
	}
	a := nilflow.NewAnalyzer()
	var all []diag.Diagnostic
	for _, pi := range r.Pkgs {
		res := a.Analyze(pi, nil)
		all = append(all, res.Diagnostics...)
	}
	all = diag.Sort(all)
	return all
}

func highLines(ds []diag.Diagnostic) map[int]int {
	m := map[int]int{}
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh {
			m[d.Position.Line]++
		}
	}
	return m
}

func TestDefiniteNilDeref(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func f() int {
	var p *int
	return *p
}
`}
	ds := analyzeSrc(t, src)
	hl := highLines(ds)
	if hl[4] != 1 {
		t.Fatalf("expected 1 high deref at line 4, got %v (%+v)", hl, ds)
	}
}

func TestExplicitNilAssignment(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func f() int {
	p := new(int)
	p = nil
	return *p
}
`}
	ds := analyzeSrc(t, src)
	if highLines(ds)[5] != 1 {
		t.Fatalf("expected deref after reassign to nil: %+v", ds)
	}
}

func TestGuardedDerefExcluded(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func f() int {
	var p *int
	if p != nil { return *p }
	if p == nil { return 0 }
	return *p
}
`}
	ds := analyzeSrc(t, src)
	// 第 4 行（保护内）不报；第 6 行在 if p==nil {return} 之后已被精化为非 nil，也不报。
	hl := highLines(ds)
	if hl[4] != 0 {
		t.Errorf("guarded deref line 4 reported: %+v", ds)
	}
	for _, d := range ds {
		if d.Position.Line == 6 {
			t.Errorf("deref after if p==nil {return} must be excluded, got %+v", d)
		}
	}
}

func TestCrossFileCall(t *testing.T) {
	files := map[string]string{
		"src/p/a.go": `package p
func deref(x *int) int { return *x }
`,
		"src/p/b.go": `package p
func f() int {
	var p *int
	return deref(p)
}
`,
	}
	ds := analyzeSrc(t, files)
	var found bool
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh && d.Position.Line == 2 &&
			len(d.CallPath) == 2 && d.CallPath[0].Position.Line == 4 &&
			d.CallPath[1].Position.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cross-file call path f(b.go:4)->deref(a.go:2): %+v", ds)
	}
}

func TestReturnValuePropagation(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func makeNil() *int { return nil }
func f() int { return *makeNil() }
`}
	ds := analyzeSrc(t, src)
	if highLines(ds)[3] != 1 {
		t.Fatalf("nil return value should propagate to deref: %+v", ds)
	}
}

func TestErrorSuccessGuard(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get() (*int, error) { x := 3; return &x, nil }
func f() int {
	p, err := get()
	if err != nil { return 0 }
	return *p
}
`}
	ds := analyzeSrc(t, src)
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh {
			t.Errorf("no defect expected on success path: %+v", d)
		}
	}
}

func TestErrorFailureBranchReports(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get() (*int, error) { return nil, nil }
func f() int {
	p, err := get()
	if err != nil { return *p }
	return 0
}
`}
	ds := analyzeSrc(t, src)
	if highLines(ds)[5] != 1 {
		t.Fatalf("deref on failure branch should be reported: %+v", ds)
	}
}

func TestBoolOkGuard(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func f(i interface{}) int {
	v, ok := i.(*int)
	if ok { return *v }
	return 0
}
`}
	ds := analyzeSrc(t, src)
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh {
			t.Errorf("type-assertion ok guard must not report: %+v", d)
		}
	}
}

func TestDifferentWritesSameBugSingleReport(t *testing.T) {
	// 同一真实缺陷，分别以直接调用与中间包装两种写法，不应重复计数。
	src := map[string]string{"src/p/a.go": `package p
func leaf(x *int) int { return *x }
func mid(x *int) int { return leaf(x) }
func f() int { var p *int; return mid(p) }
func g() int { var q *int; return leaf(q) }
`}
	ds := analyzeSrc(t, src)
	var high int
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh {
			high++
			if d.Position.Line != 2 {
				t.Errorf("defect should be located at leaf line 2, got line %d", d.Position.Line)
			}
		}
	}
	if high != 2 {
		t.Fatalf("f and g are two distinct bug sites, expected 2 reports (one per site), got %d", high)
	}
}

// 保证测试文件本身可编译（ast 引用）。
var _ = ast.NewIdent

func TestNilReceiverMethodCrossFile(t *testing.T) {
	files := map[string]string{
		"src/p/types.go": `package p
type Node struct{ Next *Node }
func (n *Node) Value() int { return *n.Next }
`,
		"src/p/svc.go": `package p
func safe() int {
	n := &Node{Next: nil}
	return n.Value()
}
func buggy() int {
	var n *Node
	return n.Value()
}
`,
	}
	ds := analyzeSrc(t, files)
	var high, safeReported int
	for _, d := range ds {
		if d.Position.Line == 3 && d.Confidence == diag.ConfidenceHigh {
			high++
			if len(d.CallPath) != 2 {
				t.Errorf("expected call path caller->method, got %v", d.CallPath)
			}
			if d.CallPath[0].Func != "src/p.buggy" {
				t.Errorf("caller should be buggy, got %s", d.CallPath[0].Func)
			}
		}
		if d.Position.Line == 6 {
			safeReported++
		}
	}
	if high != 1 {
		t.Fatalf("expected 1 high nil-receiver deref, got %d (%+v)", high, ds)
	}
	if safeReported != 0 {
		t.Errorf("non-nil receiver call must be clean")
	}
}
