package nilflow

import (
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// Caller 描述当前函数是被谁、在哪一行调用的（用于跨函数调用路径展示）。
type Caller struct {
	Func     *types.Func
	CallPos  token.Pos
	CallPath []diag.CallNode // 调用方之上的路径（不含当前函数）
}

func nodeOf(fset *token.FileSet, fn *types.Func, pos token.Pos) diag.CallNode {
	file, line, col := posAt(fset, pos)
	return diag.CallNode{
		Func:     fn.FullName(),
		Position: diag.Position{File: file, Line: line, Column: col},
	}
}

// buildCallPath 返回“根调用方 ... 当前函数”的完整节点列表。
// 约定：c.CallPath 已包含从根到调用方(c.Func)为止的节点；本函数只追加
// 当前函数内的最终节点（解引用点，或函数声明点）。
func buildCallPath(fset *token.FileSet, fn *types.Func, c *Caller, finalPos token.Pos) []diag.CallNode {
	var path []diag.CallNode
	if c != nil {
		path = append(path, c.CallPath...)
	}
	if finalPos.IsValid() {
		path = append(path, nodeOf(fset, fn, finalPos))
	} else if fn.Pos().IsValid() {
		path = append(path, nodeOf(fset, fn, fn.Pos()))
	}
	return path
}
