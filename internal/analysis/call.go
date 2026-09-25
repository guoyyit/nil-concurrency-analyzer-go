package analysis

import (
	"go/ast"
	"go/types"
)

// evalCall returns the nilness of the call's first result (Go forces any
// dereferenceable position to refer to a single value, so only single-result
// calls flow there). Provenance on specific result indices is used by the
// error-correlation refinement during statement processing.
func (e *env) evalCall(call *ast.CallExpr) evalResult {
	return evalResult{state: e.callResultState(call, 0), call: call, resultIdx: 0}
}

// callResultState models one result value of a call.
func (e *env) callResultState(call *ast.CallExpr, idx int) nilState {
	// Builtins.
	if id, ok := call.Fun.(*ast.Ident); ok {
		if b, ok := e.fn.pkg.Info.Uses[id].(*types.Builtin); ok {
			return e.builtinResult(b.Name(), call, idx)
		}
	}
	// Local function: use its current summary.
	if callee := e.pg.calleeAt(e.fn.pkg, call); callee != nil {
		return e.localCallResult(callee, call, idx)
	}
	// External library / dynamic: consult the stub table.
	return e.externalCallResult(call, idx)
}

// localCallResult applies the callee summary with the caller's argument
// states.
func (e *env) localCallResult(callee *funcDecl, call *ast.CallExpr, idx int) nilState {
	sum := callee.summary
	if sum == nil {
		return stateUnknown
	}
	// Recursion: summaries may be mid-fixpoint; Unknown is the safe initial.
	if idx >= len(sum.returns) {
		return stateUnknown
	}
	normal := sum.returns[idx]
	// If the result is a direct pass-through of one of the callee's
	// parameters, substitute the caller's argument state exactly.
	if callee.summary != nil && idx < len(callee.summary.paramReturn) {
		if pi := callee.summary.paramReturn[idx]; pi >= 0 {
			if argStates := e.argumentStates(callee, call); argStates != nil && pi < len(argStates) {
				return argStates[pi]
			}
		}
	}
	return normal
}

// argumentStates evaluates actual arguments against callee parameters.
func (e *env) argumentStates(callee *funcDecl, call *ast.CallExpr) []nilState {
	if len(call.Args) != len(callee.params) {
		// variadic mismatch: bail out of parameter sensitivity.
		return nil
	}
	out := make([]nilState, len(call.Args))
	for i, arg := range call.Args {
		out[i] = e.eval(arg).state
	}
	return out
}

// externalCallResult models return values from non-local functions.
func (e *env) externalCallResult(call *ast.CallExpr, idx int) nilState {
	var fn *types.Func
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ = e.fn.pkg.Info.Uses[fun].(*types.Func)
	case *ast.SelectorExpr:
		if sel := e.fn.pkg.Info.Selections[fun]; sel != nil && sel.Kind() == types.MethodVal {
			fn, _ = sel.Obj().(*types.Func)
		} else {
			fn, _ = e.fn.pkg.Info.Uses[fun.Sel].(*types.Func)
		}
	}
	if fn == nil {
		return stateUnknown
	}
	return stubResult(fn, idx)
}
