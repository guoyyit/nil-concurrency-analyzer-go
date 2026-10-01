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
	// Truth 仅对布尔结果有意义：沿赋值传播，供 (data, ok) 返回路径判定。
	Truth Truth
}

// CallRef 指向调用表达式及其返回值下标。
type CallRef struct {
	Expr  ast.Expr
	Idx   int
	Paths []ReturnPath // 同包被调函数的返回路径（外部调用为 nil）
}

func vNil() Val        { return Val{Kind: ValNil} }
func vNonNil() Val     { return Val{Kind: ValNonNil} }
func vUnknown() Val    { return Val{Kind: ValUnknown} }
func vTrue() Val       { return Val{Kind: ValNonNil, Truth: TruthTrue} }
func vFalse() Val      { return Val{Kind: ValNonNil, Truth: TruthFalse} }

func (v Val) isNil() bool { return v.Kind == ValNil }

// joinVal 是格上的最小上界。
//
// 关键约束：Unknown 表示“来源无法确定”（如形参、map 读取、外部调用），
// 它不是格底。Unknown 与 Nil/NonNil 合并必须降为 MaybeNil——
// 只有所有可达路径都产生空值时才允许保留 Nil，禁止因分支合并
// 把“可能非空”的值强化为“确定空”。
func joinVal(x, y Val) Val {
	if x.Kind == ValUnknown {
		if y.Kind == ValUnknown {
			return joinUnknownRefs(x, y)
		}
		// 信息缺失路径与确定路径并存：结论不得强于“可能”。
		return Val{Kind: ValMaybeNil}
	}
	if y.Kind == ValUnknown {
		return Val{Kind: ValMaybeNil}
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

// joinUnknownRefs 合并两个“来源未知”的值：未知与未知仍为未知，
// 但只有出处一致时才保留出处信息，避免把不同调用混为一谈。
func joinUnknownRefs(x, y Val) Val {
	if x.Call == y.Call {
		return x
	}
	return vUnknown()
}

// widenVal 用于“条件分支出口”的状态合并：进入分支前的状态携带的是
// 全部路径的保守结论，分支内精化是在已知条件下的收紧。合并时无条件
// 路径的结论只能让精化结论放松，绝不能把已被排除的 nil 可能性加回来。
//
// 规则：以精化值 refined 为基准；
//   - 无条件侧（outer）比 refined 更弱（Unknown/MaybeNil）时，放宽为 MaybeNil；
//   - outer 与 refined 一致或更强时，保留 refined。
func widenVal(refined, outer Val) Val {
	if outer.Kind == ValUnknown || outer.Kind == ValMaybeNil {
		if refined.Kind == ValNonNil {
			return Val{Kind: ValMaybeNil, Call: refined.Call}
		}
		return refined
	}
	return joinVal(refined, outer)
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
