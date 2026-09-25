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
		// error 为 nil（成功）：精化同一调用的其他返回值为非 nil。
		if isErrorExpr(e, info) && errRef != nil {
			refineSiblingsByRef(st, errRef, true)
		}
	} else {
		setExprVal(st, e, vNonNil(), info)
		if isErrorExpr(e, info) && errRef != nil {
			refineSiblingsByRef(st, errRef, false)
		}
	}
}

// refineSiblingsByRef 根据兄弟 error 返回值的成功/失败，精化同一调用的其它结果。
func refineSiblingsByRef(st *flowState, ref *CallRef, success bool) {
	for obj, v := range st.vars {
		if v.Call != nil && v.Call.Expr == ref.Expr && v.Call.Idx != ref.Idx {
			if success {
				if v.Kind != ValNonNil {
					st.vars[obj] = Val{Kind: ValNonNil, Call: v.Call}
				}
			} else {
				// 失败分支：仅当来源静态未知时才下降为可能 nil。
				if v.Kind == ValUnknown {
					st.vars[obj] = Val{Kind: ValMaybeNil, Call: v.Call}
				}
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
	// ok 来源于 type assertion / channel receive / map index：
	// 这些场景 ok==true 蕴含主结果非 nil（map 除外，但 map 值已为 Unknown，不强转）。
	if branch {
		ref := v.Call
		for o, w := range st.vars {
			if w.Call != nil && w.Call.Expr == ref.Expr && w.Call.Idx != ref.Idx &&
				w.Kind == ValUnknown && isOkSource(ref.Expr, ref.Idx) {
				st.vars[o] = Val{Kind: ValNonNil, Call: w.Call}
			}
		}
	}
}

// isOkSource 判断某结果是否为“value, ok”二元形式的 ok 下标（下标 1），
// 且主结果来自类型断言或通道接收（map 不蕴含非 nil）。
func isOkSource(call ast.Expr, okIdx int) bool {
	if okIdx != 1 {
		return false
	}
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
