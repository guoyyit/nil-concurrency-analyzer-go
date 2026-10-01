package nilflow_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/nilflow"
)

func TestDbgCond(t *testing.T) {
	src := map[string]string{"src/p/a.go": `package p
func get(c bool) (*int, error) {
	if c { return nil, nil }
	x := 1
	return &x, nil
}
func g(c bool) int {
	p, err := get(c)
	if err != nil { return *p }
	return 0
}
`}
	l := loader.New()
	var in []loader.InputFile
	for p, c := range src {
		in = append(in, loader.InputFile{Path: p, Content: c})
	}
	r, _ := l.Load(in)
	a := nilflow.NewAnalyzer()
	for _, pi := range r.Pkgs {
		res := a.Analyze(pi, nil)
		for k, s := range res.Summaries {
			t.Logf("SUM %s results=%v conds=%+v", k, s.Results, s.SiblingCond)
		}
	}
}
