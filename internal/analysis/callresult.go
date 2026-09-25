package analysis

import (
	"go/ast"
	"go/types"
)

// callResult models result idx of call in environment e, consulting local
// summaries, stubs and builtins.
func (r *runner) callResult(call *ast.CallExpr, idx int, e *env) nilState {
	// Builtins.
	if id, ok := call.Fun.(*ast.Ident); ok {
		if b, ok := r.fn.pkg.Info.Uses[id].(*types.Builtin); ok {
			return r.builtinState(b.Name(), idx)
		}
	}
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil {
		st := stateUnknown
		if callee.summary != nil && idx < len(callee.summary.returns) {
			st = callee.summary.returns[idx]
		}
		if callee.summary != nil && idx < len(callee.summary.paramReturn) {
			if pi := callee.summary.paramReturn[idx]; pi >= 0 {
				if args := r.argStates(callee, call, e); args != nil && pi < len(args) {
					return args[pi]
				}
			}
		}
		return st
	}
	return r.externalResult(call, idx)
}

func (r *runner) builtinState(name string, idx int) nilState {
	switch name {
	case "new", "make":
		return stateNonNil
	}
	return stateUnknown
}

func (r *runner) argStates(callee *funcDecl, call *ast.CallExpr, e *env) []nilState {
	params := callee.sig.Params()
	if params.Len() != len(call.Args) {
		return nil
	}
	out := make([]nilState, len(call.Args))
	for i, a := range call.Args {
		out[i] = e.eval(a).state
	}
	return out
}

func (r *runner) externalResult(call *ast.CallExpr, idx int) nilState {
	fn := r.externalFunc(call)
	if fn == nil {
		return stateUnknown
	}
	return stubResult(fn, idx)
}

// externalFunc resolves a non-local callee function object.
func (r *runner) externalFunc(call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ := r.fn.pkg.Info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel := r.fn.pkg.Info.Selections[fun]; sel != nil && sel.Kind() == types.MethodVal {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := r.fn.pkg.Info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

// indicatorSplit returns (successState, failureState) for a companion result,
// plus whether the call exposes an error/bool indicator and its index.
func (r *runner) indicatorSplit(call *ast.CallExpr, idx, nResults int) (ok bool, kind indicatorKind, succ, fail nilState) {
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil && callee.summary != nil {
		s := callee.summary
		if s.errIndex == nResults-1 && idx != s.errIndex {
			return true, indError, s.successReturns[idx], s.errorReturns[idx]
		}
		if s.boolIndex == nResults-1 && idx != s.boolIndex {
			return true, indBool, s.successReturns[idx], s.boolReturns[idx]
		}
		return false, 0, stateUnknown, stateUnknown
	}
	fn := r.externalFunc(call)
	if fn == nil {
		return false, 0, stateUnknown, stateUnknown
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return false, 0, stateUnknown, stateUnknown
	}
	last := sig.Results().Len() - 1
	if last < 0 || idx == last {
		return false, 0, stateUnknown, stateUnknown
	}
	if isErrorType(sig.Results().At(last).Type()) {
		succ, fail = stubCallSplit(fn, idx)
		return true, indError, succ, fail
	}
	if b, ok := sig.Results().At(last).Type().(*types.Basic); ok && b.Kind() == types.Bool {
		succ, fail = stubCallSplit(fn, idx)
		return true, indBool, succ, fail
	}
	return false, 0, stateUnknown, stateUnknown
}

// callArgs is one statically-resolved call observation during a run.
type callArgs struct {
	callee *funcDecl
	call   *ast.CallExpr
	states []nilState
}

// recordCallArgs snapshots the states of a local call's actual arguments.
func (r *runner) recordCallArgs(callee *funcDecl, call *ast.CallExpr, e *env) {
	params := callee.sig.Params()
	if params.Len() != len(call.Args) {
		return
	}
	states := make([]nilState, len(call.Args))
	for i, a := range call.Args {
		states[i] = e.eval(a).state
	}
	r.callArgs = append(r.callArgs, callArgs{callee: callee, call: call, states: states})
}
