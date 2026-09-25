package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"
)

// transferAssign 处理 = 与 := 赋值。
func (a *intraAnalyzer) transferAssign(n *ast.AssignStmt, st *flowState) *flowState {
	// 复合赋值（+= 等）不改变空值性，但其中可能有解引用。
	if n.Tok != token.DEFINE && n.Tok != token.ASSIGN {
		for _, e := range n.Rhs {
			a.eval(e, st)
		}
		a.checkLhsDeref(n.Lhs, st)
		return st
	}

	// x, y := f() 或 v, ok := m[k] / v.(T) / <-ch
	if len(n.Rhs) == 1 {
		if vals, ok := a.evalMulti(n.Rhs[0], st); ok && len(vals) == len(n.Lhs) {
			a.bindLHS(n.Lhs, vals, n.Tok == token.DEFINE, st)
			a.checkLhsDeref(n.Lhs, st)
			return st
		}
	}

	// 普通逐元素赋值。
	if len(n.Lhs) == len(n.Rhs) {
		vals := make([]Val, len(n.Rhs))
		for i, r := range n.Rhs {
			vals[i] = a.eval(r, st)
		}
		a.bindLHS(n.Lhs, vals, n.Tok == token.DEFINE, st)
		a.checkLhsDeref(n.Lhs, st)
		return st
	}

	for _, r := range n.Rhs {
		a.eval(r, st)
	}
	return st
}

func (a *intraAnalyzer) bindLHS(lhs []ast.Expr, vals []Val, define bool, st *flowState) {
	for i, l := range lhs {
		v := vals[i]
		switch e := l.(type) {
		case *ast.Ident:
			if e.Name == "_" {
				continue
			}
			if define {
				if obj := a.info.ObjectOf(e); obj != nil {
					st.vars[objectKey(obj)] = v
				} else {
					st.vars[e] = v
				}
			} else if obj := a.info.ObjectOf(e); obj != nil {
				st.vars[objectKey(obj)] = v
			} else {
				st.vars[e] = v
			}
		default:
			// 选择器/解引用等复合左值按语法节点记录（同函数内同节点共享）。
			st.exprs[l] = v
		}
	}
}

// checkLhsDeref 检查 *p = ... 与 p.f = ... 形式写入处的解引用。
func (a *intraAnalyzer) checkLhsDeref(lhs []ast.Expr, st *flowState) {
	for _, l := range lhs {
		switch e := l.(type) {
		case *ast.StarExpr:
			v := a.eval(e.X, st)
			a.checkPtrDeref(e, e.X, v, st)
		case *ast.SelectorExpr:
			sel := a.info.Selections[e]
			if sel != nil && sel.Kind() == types.FieldVal &&
				isPointerType(exprType(e.X, a.info)) {
				v := a.eval(e.X, st)
				a.checkPtrDeref(e, e.X, v, st)
			}
		case *ast.IndexExpr:
			if isPointerToArray(e.X, a.info) {
				v := a.eval(e.X, st)
				a.checkPtrDeref(e, e.X, v, st)
			}
		}
	}
}

// transferDecl 处理 var/const 声明中的初值绑定。
func (a *intraAnalyzer) transferDecl(n *ast.DeclStmt, st *flowState) *flowState {
	gd, ok := n.Decl.(*ast.GenDecl)
	if !ok {
		return st
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		// 无初值：按类型的零值初始化（指针/切片/map/chan 等为 nil）。
		if len(vs.Values) == 0 {
			for _, nm := range vs.Names {
				obj := a.info.ObjectOf(nm)
				if obj == nil {
					continue
				}
				if _, ok := st.vars[objectKey(obj)]; !ok {
					st.vars[objectKey(obj)] = zeroVal(obj.Type())
				}
			}
			continue
		}
		// 多值：var a, b = f()
		if len(vs.Values) == 1 && len(vs.Names) > 1 {
			if vals, ok := a.evalMulti(vs.Values[0], st); ok {
				lhs := make([]ast.Expr, len(vs.Names))
				for i, nm := range vs.Names {
					lhs[i] = nm
				}
				a.bindLHS(lhs, vals, true, st)
				continue
			}
		}
		for i, nm := range vs.Names {
			if i < len(vs.Values) {
				v := a.eval(vs.Values[i], st)
				if obj := a.info.ObjectOf(nm); obj != nil {
					st.vars[objectKey(obj)] = v
				}
			}
		}
	}
	return st
}

// markParamDeref 在解引用目标回溯到形参时，登记“无保护解引用”。
func (a *intraAnalyzer) markParamDeref(target ast.Expr, v Val, pos token.Pos) {
	if v.Kind == ValNonNil {
		return // 已被保护分支证明非 nil
	}
	id := rootIdent(target)
	if id == nil {
		return
	}
	obj := a.info.ObjectOf(id)
	par, ok := obj.(*types.Var)
	if !ok {
		return
	}
	sig, _ := a.env.Func.Type().(*types.Signature)
	if sig == nil {
		return
	}
	idx := -1
	if sig.Recv() == par {
		idx = 0
	} else {
		for i := 0; i < sig.Params().Len(); i++ {
			if sig.Params().At(i) == par {
				if sig.Recv() != nil {
					idx = i + 1
				} else {
					idx = i
				}
				break
			}
		}
	}
	if idx >= 0 {
		a.derefParams[idx] = true
		if a.derefPos == nil {
			a.derefPos = map[int]token.Pos{}
		}
		if _, exists := a.derefPos[idx]; !exists {
			a.derefPos[idx] = pos
		}
	}
}

func rootIdent(e ast.Expr) *ast.Ident {
	switch n := e.(type) {
	case *ast.Ident:
		return n
	case *ast.SelectorExpr:
		return rootIdent(n.X)
	case *ast.StarExpr:
		return rootIdent(n.X)
	case *ast.ParenExpr:
		return rootIdent(n.X)
	case *ast.IndexExpr:
		return rootIdent(n.X)
	}
	return nil
}
