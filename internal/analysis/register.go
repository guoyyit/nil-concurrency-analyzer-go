package analysis

import (
	"go/ast"
	"go/types"
)

// registerRels links companion lvalue variables to the indicator result of a
// multi-result call, enabling "if err != nil { use(v) }" refinement.
func (r *runner) registerRels(call *ast.CallExpr, n int, lhs []ast.Expr, e *env) {
	last := n - 1
	if last <= 0 {
		return
	}
	kind := indicatorKind(-1)
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil && callee.summary != nil {
		s := callee.summary
		switch last {
		case s.errIndex:
			kind = indError
		case s.boolIndex:
			kind = indBool
		}
	}
	if kind < 0 {
		fn := r.externalFunc(call)
		if fn != nil {
			if sig, ok := fn.Type().(*types.Signature); ok && sig.Results().Len() == n {
				t := sig.Results().At(last).Type()
				if isErrorType(t) {
					kind = indError
				} else if b, ok := t.(*types.Basic); ok && b.Kind() == types.Bool {
					kind = indBool
				}
			}
		}
	}
	if kind < 0 {
		return
	}
	indVar := lhsVar(lhs[last], e)
	if indVar == nil {
		return
	}
	for i := 0; i < last; i++ {
		val := lhsVar(lhs[i], e)
		if val == nil {
			continue
		}
		rl := &rel{val: val, call: call, idx: i, indicator: last, kind: kind}
		r.varRels[indVar] = append(r.varRels[indVar], rl)
	}
}

func lhsVar(x ast.Expr, e *env) *types.Var {
	id, ok := x.(*ast.Ident)
	if !ok || id.Name == "_" {
		return nil
	}
	return e.varOf(id)
}
