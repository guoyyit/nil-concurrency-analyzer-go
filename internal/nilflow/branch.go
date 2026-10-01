package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"
)

// refineOnCond 在进入 if 条件的某个分支前，对状态做约束精化。
// 这是错误返回值/布尔 ok 失败模式流敏感处理的核心。
func refineOnCond(st *flowState, cond ast.Expr, branch bool, info *types.Info) *flowState {
	if st == nil {
		return nil
	}
	switch n := cond.(type) {
	case *ast.ParenExpr:
		return refineOnCond(st, n.X, branch, info)

	case *ast.BinaryExpr:
		switch n.Op {
		case token.LAND:
			if branch {
				return refineOnCond(refineOnCond(st, n.X, true, info), n.Y, true, info)
			}
			// 假分支：至少一侧为假，保守合并两种可能。
			return refineOnCond(st.clone(), n.X, false, info).
				join(refineOnCond(st.clone(), n.Y, false, info))
		case token.LOR:
			if !branch {
				return refineOnCond(refineOnCond(st, n.X, false, info), n.Y, false, info)
			}
			return refineOnCond(st.clone(), n.X, true, info).
				join(refineOnCond(st.clone(), n.Y, true, info))
		case token.EQL, token.NEQ:
			return refineEquality(st, n, n.Op == token.EQL, branch, info)
		}

	case *ast.UnaryExpr:
		if n.Op == token.NOT {
			return refineOnCond(st, n.X, !branch, info)
		}

	case *ast.Ident:
		// 裸布尔条件（常见于 ok 变量）。
		if info != nil && isBoolType(info.TypeOf(n)) {
			refineBoolIdent(st, n, branch, info)
		}
	}
	return st
}

// refineEquality 处理 x == nil / x != nil / err == nil / err != nil 等。
func refineEquality(st *flowState, n *ast.BinaryExpr, isEq, branch bool, info *types.Info) *flowState {
	leftNil := isNilIdent(n.X)
	rightNil := isNilIdent(n.Y)
	switch {
	case leftNil && !rightNil:
		// nil == x
		bindEqNil(st, n.Y, isEq == branch, info)
	case rightNil && !leftNil:
		// x == nil
		bindEqNil(st, n.X, isEq == branch, info)
	default:
		// err == nilErr 以外的比较，如 err == ErrX：把两侧视作非 nil。
		if branch == isEq {
			markNonNilIfError(st, n.X, info)
			markNonNilIfError(st, n.Y, info)
		}
	}
	return st
}

// bindEqNil：nilness 为真表示该分支上表达式为 nil。
func bindEqNil(st *flowState, e ast.Expr, nilness bool, info *types.Info) {
	// 先取出调用出处，再覆盖值，否则会丢失兄弟返回值的关联。
	errRef := callRefOf(e, st, info)
	if nilness {
		setExprVal(st, e, vNil(), info)
		// error 为 nil（成功）：按摘要中的条件空值精化同一调用的其他返回值，
		// 不再无条件假设数据非 nil。
		if isErrorExpr(e, info) && errRef != nil {
			refineSiblingsByCond(st, errRef, false)
		}
	} else {
		setExprVal(st, e, vNonNil(), info)
		if isErrorExpr(e, info) && errRef != nil {
			refineSiblingsByCond(st, errRef, true)
		}
	}
}

// sameCallRef 判断两个调用出处是否指向同一次调用的（可能不同下标的）结果。
// CallRef 在绑定/读取时会产生不同副本，不能用指针相等比较。
func sameCallRef(a, b *CallRef) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Expr == b.Expr
}

// siblingCondFor 返回某调用结果相对指定兄弟下标的条件摘要；wantErr 限定
// 兄弟必须是 error（true）或 bool（false）。
func siblingCondFor(call *CallRef, sibIdx int, wantErr bool) *SiblingCond {
	if call == nil || call.Cond == nil {
		return nil
	}
	c := call.Cond
	if c.SibIdx == sibIdx && c.SibIsErr == wantErr {
		return c
	}
	return nil
}

// refineSiblingsByCond 根据兄弟 error 在本分支的真假，按被调函数摘要给出的
// 条件空值精化同一调用的其它结果：
//   - err == nil（success 分支）：采用 WhenFalse。所有成功路径数据都为 nil 时
//     得到确定 nil，部分路径为 nil 时得到可能 nil，来源未知则保持未知；
//   - err != nil（failure 分支）：采用 WhenTrue。没有真实 nil 失败路径时，
//     摘要已将其收敛为非 nil，从而不把受保护的解引用误报为缺陷。
func refineSiblingsByCond(st *flowState, ref *CallRef, errIsNonNil bool) {
	var fallback Val
	haveFallback := false
	if ref.Cond != nil && ref.Cond.SibIsErr {
		fallback = ref.Cond.WhenFalse
		haveFallback = true
		if errIsNonNil {
			fallback = ref.Cond.WhenTrue
		}
	}
	for obj, v := range st.vars {
		if v.Call != nil && sameCallRef(v.Call, ref) && v.Call.Idx != ref.Idx {
			nv := v
			// 为“该数据结果”选取它自己相对 error 兄弟的条件（被调摘要按
			// (数据下标, 兄弟下标) 分别记录），不能复用 error 自身携带的 Cond。
			if cd := siblingCondFor(v.Call, ref.Idx, true); cd != nil {
				bucket := cd.WhenFalse
				if errIsNonNil {
					bucket = cd.WhenTrue
				}
				nv = Val{Kind: bucket.Kind, Call: v.Call}
			} else if haveFallback {
				nv = Val{Kind: fallback.Kind, Call: v.Call}
			} else if errIsNonNil {
				// 无摘要（外部/递归函数）：失败分支仅把来源未知降为可能 nil。
				if v.Kind == ValUnknown {
					nv = Val{Kind: ValMaybeNil, Call: v.Call}
				}
			}
			// 无摘要的成功分支：保持原值，绝不凭空推断数据非 nil。
			if nv.Kind != v.Kind {
				st.vars[obj] = nv
			}
		}
	}
}

func refineBoolIdent(st *flowState, id *ast.Ident, branch bool, info *types.Info) {
	obj := info.ObjectOf(id)
	if obj == nil {
		return
	}
	v, ok := st.vars[objectKey(obj)]
	if !ok {
		return
	}
	if v.Call == nil {
		// 普通布尔变量：条件为真不带来空值信息。
		return
	}
	refineOkSiblings(st, v.Call, branch)
}

// refineOkSiblings 根据同一次调用的布尔 ok 在本分支的真假，精化其数据结果。
// 保护只作用于同一次调用产生的数据（按 CallRef.Expr 匹配）：
//   - 调用带摘要（函数返回 (value, ok)）：ok==true 采用 WhenTrue。
//     若 true 路径可能返回 nil 数据，则保留可能 nil（仍会报告）；
//     来源未知时按 ok 惯用法精化为非 nil（受保护使用，不报告，可通过证据查看）。
//     ok==false 分支采用 WhenFalse；
//   - 内建 value,ok（类型断言/通道接收；map 值不蕴含非 nil，保守保留）：
//     沿用原语义，ok==true 把来源未知的数据精化为非 nil。
func refineOkSiblings(st *flowState, ref *CallRef, okIsTrue bool) {
	for o, w := range st.vars {
		if w.Call == nil || w.Call.Expr != ref.Expr || w.Call.Idx == ref.Idx {
			continue
		}
		var nv Val
		if cd := siblingCondFor(w.Call, ref.Idx, false); cd != nil {
			bucket := cd.WhenFalse
			if okIsTrue {
				bucket = cd.WhenTrue
			}
			switch bucket.Kind {
			case ValNil:
				// 即便 ok 为真也确定返回 nil：按规则作为可能 nil 报告，
				// 不静默（“ok 为真仍可能为空数据时应报告”）。
				nv = Val{Kind: ValMaybeNil, Call: w.Call}
			case ValMaybeNil:
				nv = Val{Kind: ValMaybeNil, Call: w.Call}
			case ValNonNil:
				nv = Val{Kind: ValNonNil, Call: w.Call}
			default:
				// 来源未知：受 ok 惯用法保护，精化为非 nil。
				nv = Val{Kind: ValNonNil, Call: w.Call}
			}
		} else if okIsTrue && isOkBuiltinSource(ref.Expr) {
			if w.Kind == ValUnknown {
				nv = Val{Kind: ValNonNil, Call: w.Call}
			} else {
				nv = w
			}
		} else {
			continue
		}
		if nv.Kind != w.Kind {
			st.vars[o] = nv
		}
	}
}

// isOkBuiltinSource 判断调用是否为“value, ok”内建形式（类型断言/通道接收）。
// map 索引的 ok 不蕴含 value 非 nil，故不在此列。
func isOkBuiltinSource(call ast.Expr) bool {
	switch call.(type) {
	case *ast.TypeAssertExpr:
		return true
	}
	if u, ok := call.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		return true
	}
	return false
}

func markNonNilIfError(st *flowState, e ast.Expr, info *types.Info) {
	if isErrorExpr(e, info) {
		setExprVal(st, e, vNonNil(), info)
	}
}

func isErrorExpr(e ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	t := info.TypeOf(e)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() == nil && obj.Name() == "error"
}

func isBoolType(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind() == types.Bool
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// setExprVal 更新表达式（当前仅支持标识符及其对象）的空值。
func setExprVal(st *flowState, e ast.Expr, v Val, info *types.Info) {
	if id, ok := e.(*ast.Ident); ok {
		if obj := info.ObjectOf(id); obj != nil {
			st.vars[objectKey(obj)] = v
			return
		}
	}
	st.exprs[e] = v
}

func callRefOf(e ast.Expr, st *flowState, info *types.Info) *CallRef {
	if id, ok := e.(*ast.Ident); ok {
		if obj := info.ObjectOf(id); obj != nil {
			if v, ok := st.vars[objectKey(obj)]; ok {
				return v.Call
			}
			return nil
		}
	}
	if v, ok := st.exprs[e]; ok {
		return v.Call
	}
	return nil
}
