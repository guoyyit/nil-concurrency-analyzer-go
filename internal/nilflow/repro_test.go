package nilflow_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

func dump(ds []diag.Diagnostic) string {
	var b []byte
	for _, d := range ds {
		b = append(b, d.Position.File...)
		b = append(b, ':')
		if d.Confidence == diag.ConfidenceHigh {
			b = append(b, 'H')
		} else {
			b = append(b, 'S')
		}
		b = append(b, ' ')
	}
	return string(b)
}

// 形态一：部分分支返回 nil，其他分支返回来源不确定的值。
func TestReproBranchMerge(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(m map[string]*int, k string) *int {
	if k == "" {
		return nil
	}
	return m[k]
}
func f(m map[string]*int) int {
	p := get(m, "x")
	return *p
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("branch-merge diags: %+v", ds)
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh && d.Position.Line == 10 {
			t.Errorf("issue1: deref must not be definite: %+v", d)
		}
	}
}

// 形态二：(data, error) 存在 data==nil, err==nil 路径。
func TestReproErrorNilNil(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(cond bool) (*int, error) {
	if cond {
		return nil, nil
	}
	x := 3
	return &x, nil
}
func f(cond bool) int {
	p, err := get(cond)
	if err != nil {
		return 0
	}
	return *p
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("error-nilnil diags: %+v", ds)
	var reported bool
	for _, d := range ds {
		if d.Position.Line == 14 {
			reported = true
		}
	}
	if !reported {
		t.Errorf("issue2: deref after err-only guard must be reported: %+v", ds)
	}
}

// 形态三：(data, ok bool)，ok==false 提前退出。
func TestReproBoolOkGuard(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func lookup(cond bool) (*int, bool) {
	if cond {
		x := 3
		return &x, true
	}
	return nil, false
}
func f(cond bool) int {
	p, ok := lookup(cond)
	if !ok {
		return 0
	}
	return *p
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("bool-ok diags: %+v", ds)
	for _, d := range ds {
		if d.Position.Line == 14 {
			t.Errorf("issue3: guarded use must not be reported at all: %+v", d)
		}
	}
}

// 形态二的确定版本：所有 err==nil 路径 data 都为 nil。
func TestReproErrorNilNilDefinite(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
type myErr struct{}
func (myErr) Error() string { return "" }
func get(cond bool) (*int, error) {
	if cond {
		return nil, myErr{}
	}
	return nil, nil
}
func f(cond bool) int {
	p, err := get(cond)
	if err != nil {
		return 0
	}
	return *p
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("error-nilnil-def diags: %+v", ds)
	var high bool
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh && d.Position.Line == 15 {
			high = true
		}
	}
	if !high {
		t.Errorf("issue2-definite: deref must be definite: %s", dump(ds))
	}
}

// 形态三反例：ok==true 的路径上数据仍可能为 nil，必须报 possible。
func TestReproBoolOkTrueStillNil(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func lookup(m map[string]*int, k string) (*int, bool) {
	if k == "" {
		return nil, false
	}
	return m[k], true
}
func f(m map[string]*int) int {
	p, ok := lookup(m, "x")
	if !ok {
		return 0
	}
	return *p
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("ok-true-maybe diags: %+v", ds)
	var poss bool
	for _, d := range ds {
		if d.Position.Line == 14 && d.Certainty == diag.CertaintyPossible {
			poss = true
		}
	}
	if !poss {
		t.Errorf("ok==true path with unknown-source data must be possible: %+v", ds)
	}
}

// err != nil 分支内解引用属于“失败分支”，只有该分支数据确实为 nil 才报。
func TestReproErrorNonNilBranchNoFalsePositive(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
type myErr2 struct{}
func (myErr2) Error() string { return "" }
func get(cond bool) (*int, error) {
	if cond {
		x := 3
		return &x, myErr2{}
	}
	return nil, nil
}
func f(cond bool) int {
	p, err := get(cond)
	if err != nil {
		return *p
	}
	return 0
}
`}
	ds := analyzeSrc(t, src)
	t.Logf("err-nonnil branch diags: %+v", ds)
	for _, d := range ds {
		if d.Position.Line == 14 {
			t.Errorf("non-nil data on error branch must not be reported: %+v", d)
		}
	}
}
