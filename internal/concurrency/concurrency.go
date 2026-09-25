// Package concurrency 识别常见的并发使用缺陷：
//   - mutex-copy：互斥/同步原语按值复制（高置信，函数签名级为可疑）；
//   - mutex-not-unlocked：加锁后某退出路径未解锁、重复加解锁（区分高置信/可疑）；
//   - nil-channel-send：向空通道发送造成永久阻塞（确定为高置信，可能为可疑）。
package concurrency

import (
	"go/ast"
	"go/token"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/nilflow"
)

// Analyzer 对一个包做并发缺陷分析。
//
// copylock 是包级轻量检查，mutex 路径是过程内轻量遍历，二者每轮在全新
// FileSet 上整体重算（不能跨轮缓存 token.Pos，否则位置会失配）；
// nil-channel-send 的数据流状态来自 nilflow（已做函数级增量）。
type Analyzer struct{}

func NewAnalyzer() *Analyzer { return &Analyzer{} }

type nilChanAdapter struct {
	res *nilflow.PackageResult
}

func (n nilChanAdapter) SendChanKind(pos token.Pos) (int, bool) {
	k, ok := n.res.SendStates[pos]
	return int(k), ok
}

func (n nilChanAdapter) Sends() map[token.Pos]*ast.SendStmt { return n.res.Sends }

// Analyze 对单个包执行并发检查，返回诊断。
func (a *Analyzer) Analyze(pi *loader.PkgInfo, nr *nilflow.PackageResult) []diag.Diagnostic {
	var out []diag.Diagnostic
	out = append(out, checkCopylocks(pi.Fset, pi.TypeInfo, orderedFiles(pi))...)

	for _, fi := range pi.Ordered {
		for _, decl := range fi.File.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			out = append(out, checkMutexPaths(fd, pi.Fset, pi.TypeInfo)...)
		}
	}

	out = append(out, checkNilChanSend(nilChanAdapter{res: nr}, pi.Fset)...)
	return out
}

func orderedFiles(pi *loader.PkgInfo) []*ast.File {
	var fs []*ast.File
	for _, fi := range pi.Ordered {
		fs = append(fs, fi.File)
	}
	return fs
}
