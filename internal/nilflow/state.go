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
	// Cond 是该返回值相对某个 error/ok 兄弟返回值的条件空值（来自被调摘要）。
	Cond *SiblingCond
}

func vNil() Val     { return Val{Kind: ValNil} }
func vNonNil() Val  { return Val{Kind: ValNonNil} }
func vUnknown() Val { return Val{Kind: ValUnknown} }

func (v Val) isNil() bool { return v.Kind == ValNil }

// joinVal 是格上的最小上界。
//
// 关键约束（分支合并规则）：结论必须反映全部可达路径。只有所有可达路径
// 都产生确定 nil 时，合并才允许是 ValNil；只要存在一条路径上的值可能非空
// 或来源无法确定（ValUnknown），合并就必须降为 ValMaybeNil，绝不能把
// “确定 nil”与“未知”合并后强化成确定 nil。
func joinVal(x, y Val) Val {
	if x.Kind == ValUnknown {
		// 未知 ⊔ 未知 仍是未知（保留后值出处）；未知 ⊔ 可能 nil 仍是可能 nil；
		// 未知 ⊔ 确定 nil/非 nil：未知路径可能与确定路径相反，结论降为可能 nil，
		// 绝不强化为确定值。
		if y.Kind == ValUnknown || y.Kind == ValMaybeNil {
			return y
		}
		return Val{Kind: ValMaybeNil}
	}
	if y.Kind == ValUnknown {
		if x.Kind == ValMaybeNil {
			return x
		}
		return Val{Kind: ValMaybeNil}
	}
	if x.Kind == y.Kind {
		if x.Call == y.Call {
			return x
		}
		// 来源不同，合并后保留来源信息意义不大，按 Kind 处理。
		return Val{Kind: x.Kind}
	}
	if x.Kind == ValMaybeNil || y.Kind == ValMaybeNil {
		return Val{Kind: ValMaybeNil}
	}
	// ValNil ⊔ ValNonNil：部分路径为空、部分路径非空 -> 可能 nil。
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
