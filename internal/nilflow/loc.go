package nilflow

import (
	"go/token"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// loc 是与具体 token.FileSet 无关的源码位置。
//
// 增量重分析使用全新 FileSet，函数可能整体平移行号。除绝对 file/line/column
// （分析当轮写入）外，记录所属函数 fn 与“相对函数声明起始行的偏移 offset”，
// 重放时用新 AST 同名函数的声明行 + offset 重算行号。
type loc struct {
	file   string
	line   int
	column int

	fn     string // 所属/锚定函数全名
	offset int    // 锚定函数声明起始行到该位置的行偏移
}

type locPathNode struct {
	fn  string
	loc loc
}

type locDiag struct {
	cat  diag.Category
	loc  loc
	msg  string
	conf diag.Confidence
	cert diag.Certainty
	path []locPathNode
}

func locAt(fset *token.FileSet, pos token.Pos) loc {
	p := fset.Position(pos)
	return loc{file: p.Filename, line: p.Line, column: p.Column}
}

// fnStart 是某函数在本轮 FileSet 中的声明锚点。
type fnStart struct {
	file string
	line int
}

// toLocDiag 把本轮诊断转为可跨轮缓存的形式。
// mainFn / mainStart 是产生该诊断的函数及其当轮声明位置；
// pathStarts 提供调用路径上各函数节点的当轮声明位置。
func toLocDiag(d diag.Diagnostic, mainFn string, mainStart fnStart,
	pathStarts []fnStart) locDiag {

	ml := loc{file: d.Position.File, line: d.Position.Line, column: d.Position.Column}
	if mainStart.file == ml.file {
		ml.fn = mainFn
		ml.offset = ml.line - mainStart.line
	}
	ld := locDiag{
		cat:  d.Category,
		loc:  ml,
		msg:  d.Message,
		conf: d.Confidence,
		cert: d.Certainty,
	}
	for i, n := range d.CallPath {
		nl := loc{file: n.Position.File, line: n.Position.Line, column: n.Position.Column, fn: n.Func}
		if i < len(pathStarts) && pathStarts[i].file == nl.file {
			nl.offset = nl.line - pathStarts[i].line
		}
		ld.path = append(ld.path, locPathNode{fn: n.Func, loc: nl})
	}
	return ld
}

// locToPosition 在本轮 FileSet 中还原位置；anchors 为函数全名 -> 新声明锚点。
func locToPosition(fset *token.FileSet, l loc, anchors map[string]fnStart) diag.Position {
	line := l.line
	if l.fn != "" {
		if fs, ok := anchors[l.fn]; ok && fs.file == l.file {
			line = fs.line + l.offset
		}
	}
	var tf *token.File
	fset.Iterate(func(f *token.File) bool {
		if f.Name() == l.file {
			tf = f
			return false
		}
		return true
	})
	if tf != nil && line >= 1 && line <= tf.LineCount() {
		pp := fset.Position(tf.LineStart(line) + token.Pos(l.column-1))
		return diag.Position{File: pp.Filename, Line: pp.Line, Column: pp.Column}
	}
	return diag.Position{File: l.file, Line: line, Column: l.column}
}

// locsToDiagnostics 在本轮 FileSet/AST 中还原缓存诊断。
func locsToDiagnostics(fset *token.FileSet, lds []locDiag, mainFn string,
	anchors map[string]fnStart) []diag.Diagnostic {

	out := make([]diag.Diagnostic, 0, len(lds))
	for _, ld := range lds {
		main := ld.loc
		if main.fn == "" {
			main.fn = mainFn
		}
		d := diag.Diagnostic{
			Category:   ld.cat,
			Message:    ld.msg,
			Confidence: ld.conf,
			Certainty:  ld.cert,
			Position:   locToPosition(fset, main, anchors),
		}
		for _, n := range ld.path {
			d.CallPath = append(d.CallPath, diag.CallNode{
				Func:     n.fn,
				Position: locToPosition(fset, n.loc, anchors),
			})
		}
		out = append(out, d)
	}
	return out
}
