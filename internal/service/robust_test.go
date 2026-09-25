package service_test

import (
	"testing"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/service"
)

func TestEmptyInput(t *testing.T) {
	ds, err := service.New().Analyze(nil, service.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 0 {
		t.Fatalf("empty input should yield 0 diag, got %d", len(ds))
	}
}

func TestSyntaxErrorDoesNotCrash(t *testing.T) {
	src := "package p\nfunc f( { this is broken !!!\n"
	_, err := service.New().Analyze([]loader.InputFile{{Path: "x/a.go", Content: src}},
		service.DefaultOptions())
	// 语法彻底无法解析时返回错误；部分可解析时返回（可能为空的）诊断，二者都不应 panic。
	_ = err
}

func TestExternalCallUnknownSilent(t *testing.T) {
	src := `package p
import "os"
func f() {
	c := make(chan os.Signal, 1)
	_ = c
}
`
	ds, err := service.New().Analyze([]loader.InputFile{{Path: "x/a.go", Content: src}},
		service.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Confidence == "high" {
			t.Errorf("external/unknown code should not produce high diag: %+v", d)
		}
	}
}

func TestDeterministicAcrossManyRuns(t *testing.T) {
	src := "package p\nfunc f() int { var p *int; return *p }\n"
	var prev []byte
	for i := 0; i < 5; i++ {
		ds, _ := service.New().Analyze(
			[]loader.InputFile{{Path: "x/a.go", Content: src}},
			service.DefaultOptions())
		cur := []byte{}
		for _, d := range ds {
			cur = append(cur, []byte(d.Position.File)...)
			cur = append(cur, byte(d.Position.Line), byte(d.Position.Column))
		}
		if prev != nil && string(cur) != string(prev) {
			t.Fatal("non-deterministic output across cold runs")
		}
		prev = cur
	}
}
