package analysis

import (
	"go/ast"
	"go/types"
)

// checkCall checks receiver/argument nil dereferences for a call.
// asStmt indicates the call is used as a statement (no results used).
func (r *runner) checkCall(call *ast.CallExpr, e *env, asStmt bool) {
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil {
		r.recordCallArgs(callee, call, e)
	}
	// Receiver implicit dereference for method calls.
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if s := r.fn.pkg.Info.Selections[sel]; s != nil && s.Kind() == types.MethodVal {
			if _, isPtr := r.fn.pkg.Info.TypeOf(sel.X).(*types.Pointer); isPtr {
				st := e.eval(sel.X).state
				if st.mayBeNil() {
					r.reportDeref(call, sel.X, st, e, "method call on possibly-nil pointer")
				}
			}
			r.checkExpr(sel.X, e)
		}
	}
	for _, a := range call.Args {
		r.checkExpr(a, e)
	}
	// Fun itself when it's an expression (function value calls) is not a
	// dereference; nil func values panic with a different error and are out of
	// scope.
}
