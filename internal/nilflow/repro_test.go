package nilflow_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// 形态一：部分分支返回 nil，其余分支返回来源不确定（map 读取）值，
// 合并后不得升级为确定空指针，只允许可能/静默。
func TestReproBranchMergeNotDefinite(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(m map[string]*int, c bool) *int {
	if c { return nil }
	return m["x"]
}
func f(m map[string]*int) int {
	return *get(m, true)
}
`}
	ds := analyzeSrc(t, src)
	for _, d := range ds {
		if d.Confidence == diag.ConfidenceHigh {
			t.Fatalf("branch merge must not produce definite nil deref: %+v", d)
		}
	}
}

// 形态二：存在数据 nil、错误也 nil 的返回路径，err==nil 保护后的解引用必须报告；
// err!=nil 分支内的解引用不得误报。
func TestReproDataNilErrorNilReported(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(c bool) (*int, error) {
	if c { return nil, nil }
	x := 1
	return &x, nil
}
func f(c bool) int {
	p, err := get(c)
	if err != nil { return 0 }
	return *p
}
func g(c bool) int {
	p, err := get(c)
	if err != nil { return *p }
	return 0
}
`}
	ds := analyzeSrc(t, src)
	// f: err==nil 保护后解引用，部分 err==nil 路径数据为 nil -> possible（第 10 行）。
	var possibleAt10 bool
	for _, d := range ds {
		if d.Position.Line == 10 {
			possibleAt10 = true
		}
		// g: err!=nil 分支内的解引用（第 14 行）不得误报。
		if d.Position.Line == 14 {
			t.Fatalf("deref inside err!=nil branch must not be reported: %+v", d)
		}
	}
	if !possibleAt10 {
		t.Fatalf("deref after err==nil guard with nil,nil path must be reported: %+v", ds)
	}
}

// 形态二b：所有 err==nil 路径数据都为 nil -> 确定。
func TestReproDataNilAllNilErrorDefinite(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(c bool) (*int, error) {
	if c { return nil, nil }
	return nil, nil
}
func f(c bool) int {
	p, err := get(c)
	if err != nil { return 0 }
	return *p
}
`}
	ds := analyzeSrc(t, src)
	if highLines(ds)[9] != 1 {
		t.Fatalf("all err==nil paths nil data must be definite: %+v", ds)
	}
}

// 形态三：布尔存在性保护后的使用不得报告。
func TestReproBoolOkGuardSilent(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func lookup() (*int, bool) { return nil, false }
func f() int {
	p, ok := lookup()
	if !ok { return 0 }
	return *p
}
`}
	ds := analyzeSrc(t, src)
	for _, d := range ds {
	if d.Position.Line == 7 {
			t.Fatalf("bool ok guarded deref must not be reported: %+v", d)
		}
	}
}
