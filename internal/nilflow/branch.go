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
	if nilness {
		setExprVal(st, e, vNil(), info)
	} else {
		setExprVal(st, e, vNonNil(), info)
	}
	// err == nil / err != nil：绝不能由“错误为空”反推“数据非空”。
	// 正确做法是按被调函数的返回路径过滤：只保留满足该 error 取值的路径，
	// 再用这些路径上数据值的合并精化同一调用的兄弟结果。
	if isErrorExpr(e, info) {
		if ref := callRefOf(e, st, info); ref != nil {
			refineSiblingsByError(st, ref, nilness)
		}
	}
}

// refineSiblingsByError 按 error 兄弟结果在本分支上的取值（nilness：
// true=err 为 nil，false=err 非 nil）过滤被调函数返回路径，并据此
// 精化同一调用产生的其它结果。
func refineSiblingsByError(st *flowState, ref *CallRef, errIsNil bool) {
	var definite, unresolved []ReturnPath
	for _, p := range ref.Paths {
		if ref.Idx >= len(p.Vals) {
			continue
		}
		k := p.Vals[ref.Idx].Kind
		if k == ValUnknown {
			// error 来源无法确定的路径在两个分支上都可能可达。
			unresolved = append(unresolved, p)
			continue
		}
		if errIsNil == (k == ValNil) {
			definite = append(definite, p)
		}
	}
	// 优先使用静态确定落在该分支的路径（这是收紧的依据）；
	// 只有不存在任何确定路径时，才把无法判定 error 的路径视为可达，
	// 避免用失败/成功分支的确定数据污染另一侧结论（宁可沉默，不可滥报）。
	matches := definite
	if len(matches) == 0 {
		matches = unresolved
	}
	for obj, v := range st.vars {
		if v.Call != nil && v.Call.Expr == ref.Expr && v.Call.Idx != ref.Idx {
			refineSiblingOnPaths(st, obj, v, matches)
		}
	}
}

type pathPred func(ReturnPath) bool

func filterPaths(paths []ReturnPath, pred pathPred) []ReturnPath {
	var out []ReturnPath
	for _, p := range paths {
		if pred(p) {
			out = append(out, p)
		}
	}
	return out
}

// refineSiblingOnPaths 用过滤后的返回路径合并值精化兄弟结果；
// 无路径信息（外部调用/递归未知）时保持原状，宁可沉默也不臆断非空。
func refineSiblingOnPaths(st *flowState, obj interface{}, v Val, paths []ReturnPath) {
	if len(paths) == 0 {
		return
	}
	var joined Val
	found := false
	for _, p := range paths {
		if v.Call.Idx >= len(p.Vals) {
			continue
		}
		np := p.Vals[v.Call.Idx]
		np.Call = v.Call
		if !found {
			joined = np
			found = true
		} else {
			joined = joinVal(joined, np)
		}
	}
	// 分支条件把可达路径过滤为子集，直接用子集合并值替换是合法的收紧；
	// 无子集匹配（该分支静态不可达）时保持原状，不做推断。
	if found {
		st.vars[obj] = joined
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
	ref := v.Call
	want := TruthTrue
	if !branch {
		want = TruthFalse
	}
	if ref != nil {
		switch ref.Expr.(type) {
		case *ast.CallExpr:
			// ok 来源于函数调用（含多层包装）：按返回路径上的布尔真假过滤。
			var definite, unresolved []ReturnPath
			for _, p := range ref.Paths {
				if ref.Idx >= len(p.Truths) {
					continue
				}
				switch p.Truths[ref.Idx] {
				case want:
					definite = append(definite, p)
				case TruthUnknown:
					unresolved = append(unresolved, p)
				}
			}
			// 确定路径优先；无确定匹配时才保留真假未知的路径。
			matches := definite
			if len(matches) == 0 {
				matches = unresolved
			}
			for o, w := range st.vars {
				if w.Call != nil && w.Call.Expr == ref.Expr && w.Call.Idx != ref.Idx {
					refineSiblingOnPaths(st, o, w, matches)
				}
			}
		default:
			// 内置 (value, ok) 形式：type assertion / channel receive / map index。
			// ok==true 蕴含主结果非 nil（map 值静态为 Unknown，精化仅消除误报）；
			// 保护严格限定为同一次表达式产生的主结果，绝不牵连其它值。
			if branch {
				refineBuiltinOkSibling(st, ref.Expr, ref.Idx)
			}
		}
	}
}

func markNonNilIfError(st *flowState, e ast.Expr, info *types.Info) {
	if isErrorExpr(e, info) {
		setExprVal(st, e, vNonNil(), info)
	}
}

// refineBuiltinOkSibling 将内置二元形式 ok==true 分支上的主结果精化为非 nil。
// 保护严格限定为同一次表达式产生的主结果（Call.Expr 相同、Idx 不同）。
func refineBuiltinOkSibling(st *flowState, src ast.Expr, okIdx int) {
	for obj, w := range st.vars {
		if w.Call != nil && w.Call.Expr == src && w.Call.Idx != okIdx {
			if w.Kind == ValUnknown {
				st.vars[obj] = Val{Kind: ValNonNil, Call: w.Call}
			}
		}
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
