package nilflow

import (
	"go/ast"
	"go/token"
)

// ValKind 是空值格的元素。
type ValKind int

const (
	ValUnknown  ValKind = iota // 无信息
	ValNil                     // 确定 nil
	ValNonNil                  // 确定非 nil
	ValMaybeNil                // 可能为 nil（流合并）
)

// Val 是一个抽象空值。Call 在值来源于调用返回值时记录其出处，
// 以便根据 error/ok 兄弟返回值在条件分支上做精化。
type Val struct {
	Kind ValKind
	Call *CallRef
}

// CallRef 指向调用表达式及其返回值下标。
type CallRef struct {
	Expr *ast.CallExpr
	Idx  int
}

func vNil() Val     { return Val{Kind: ValNil} }
func vNonNil() Val  { return Val{Kind: ValNonNil} }
func vUnknown() Val { return Val{Kind: ValUnknown} }

func (v Val) isNil() bool { return v.Kind == ValNil }

// joinVal 是格上的最小上界。
func joinVal(x, y Val) Val {
	if x.Kind == ValUnknown {
		return y
	}
	if y.Kind == ValUnknown {
		return x
	}
	if x.Kind == y.Kind {
		if x.Call == y.Call {
			return x
		}
		// 来源不同，合并后保留来源信息意义不大，按 Kind 处理。
		return Val{Kind: x.Kind}
	}
	return Val{Kind: ValMaybeNil}
}

// flowState 是程序点处的空值环境。
// key 使用 *ast.Object（标识符的语义实体），保证跨语句稳定。
type flowState struct {
	vars map[interface{}]Val
	// exprs 记录“调用结果临时变量”等按语法节点的值。
	exprs map[ast.Expr]Val
}

func newFlowState() *flowState {
	return &flowState{vars: map[interface{}]Val{}, exprs: map[ast.Expr]Val{}}
}

func (s *flowState) clone() *flowState {
	c := newFlowState()
	for k, v := range s.vars {
		c.vars[k] = v
	}
	for k, v := range s.exprs {
		c.exprs[k] = v
	}
	return c
}

func (s *flowState) join(o *flowState) *flowState {
	m := newFlowState()
	for k, v := range s.vars {
		if w, ok := o.vars[k]; ok {
			m.vars[k] = joinVal(v, w)
		} else {
			m.vars[k] = v
		}
	}
	for k, v := range o.vars {
		if _, ok := s.vars[k]; !ok {
			m.vars[k] = v
		}
	}
	for k, v := range s.exprs {
		if w, ok := o.exprs[k]; ok {
			m.exprs[k] = joinVal(v, w)
		}
	}
	for k, v := range o.exprs {
		if _, ok := s.exprs[k]; !ok {
			m.exprs[k] = v
		}
	}
	return m
}

// posAt 返回带文件名的位置字符串。
func posAt(fset *token.FileSet, p token.Pos) (file string, line, col int) {
	pos := fset.Position(p)
	return pos.Filename, pos.Line, pos.Column
}
