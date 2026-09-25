package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// env is a flow-sensitive binding of variables to nilness states.
//
// Branching copies the underlying map; merges join maps pointwise.
type env struct {
	fn *funcDecl
	pg *Program
	m  map[*types.Var]nilState
	// lockFlow tracks locked/unlocked state per static lock target.
	lockFlow map[string]lockFlowState
	// guarded records variables proven non-nil by an explicit guard on the
	// current path.
	guarded map[*types.Var]bool
}

func (p *Program) newEnv(fn *funcDecl) *env {
	return &env{
		fn:       fn,
		pg:       p,
		m:        map[*types.Var]nilState{},
		lockFlow: map[string]lockFlowState{},
		guarded:  map[*types.Var]bool{},
	}
}

func (e *env) fork() *env {
	cp := &env{
		fn:       e.fn,
		pg:       e.pg,
		m:        make(map[*types.Var]nilState, len(e.m)+2),
		lockFlow: make(map[string]lockFlowState, len(e.lockFlow)+2),
		guarded:  make(map[*types.Var]bool, len(e.guarded)+2),
	}
	for k, v := range e.m {
		cp.m[k] = v
	}
	for k, v := range e.lockFlow {
		cp.lockFlow[k] = v
	}
	for k := range e.guarded {
		cp.guarded[k] = true
	}
	return cp
}

// get returns the state of v, Unknown when unbound.
func (e *env) get(v *types.Var) nilState {
	if v == nil {
		return stateUnknown
	}
	if s, ok := e.m[v]; ok {
		return s
	}
	return stateUnknown
}

func (e *env) set(v *types.Var, s nilState) {
	if v == nil {
		return
	}
	e.m[v] = s
}

func (e *env) markGuarded(v *types.Var) {
	if v != nil {
		e.guarded[v] = true
	}
}

func (e *env) isGuarded(v *types.Var) bool { return e.guarded[v] }

// guardedExpr reports whether the value x is a variable proven non-nil by a
// guard on the current path.
func (e *env) guardedExpr(x ast.Expr) *types.Var {
	id, ok := x.(*ast.Ident)
	if !ok {
		return nil
	}
	v := e.varOf(id)
	if v != nil && e.guarded[v] {
		return v
	}
	return nil
}

// mergeVars joins the states of the same variable across environments.
// Environments may represent different paths into one join point.
func mergeEnvs(list []*env) *env {
	if len(list) == 0 {
		return nil
	}
	out := list[0].fork()
	for _, other := range list[list0Extra(list):] {
		if other == nil {
			continue
		}
		for v, s := range other.m {
			out.m[v] = merge(out.get(v), s)
		}
		for k, st := range other.lockFlow {
			out.lockFlow[k] = mergeLock(out.lockFlow[k], st)
		}
	}
	return out
}

func list0Extra(list []*env) int {
	if len(list) == 0 {
		return 0
	}
	return 1
}

// varOf resolves the variable an lvalue identifier refers to.
func (e *env) varOf(id *ast.Ident) *types.Var {
	if id == nil {
		return nil
	}
	if v, ok := e.fn.pkg.Info.Uses[id].(*types.Var); ok {
		return v
	}
	if v, ok := e.fn.pkg.Info.Defs[id].(*types.Var); ok {
		return v
	}
	return nil
}

// evalResult pairs the nil state of an expression value with, when the value
// is one of a call's result values, provenance about that call.
type evalResult struct {
	state nilState
	// callResult identifies a result of a call expression: the call node and
	// its result index, allowing error-correlation refinement.
	call      *ast.CallExpr
	resultIdx int
}

// eval computes the nilness state of expression x in environment e.
func (e *env) eval(x ast.Expr) evalResult {
	switch t := x.(type) {
	case *ast.Ident:
		if t.Name == "nil" {
			// The predeclared nil has no Uses object in typed packages.
			return evalResult{state: stateNil}
		}
		if v := e.varOf(t); v != nil {
			return evalResult{state: e.get(v)}
		}
		return evalResult{state: stateUnknown}

	case *ast.ParenExpr:
		return e.eval(t.X)

	case *ast.UnaryExpr:
		if t.Op == token.AND { // &x, &T{}
			return evalResult{state: stateNonNil}
		}
		if t.Op == token.ARROW { // <-ch: not a reference type we model
			return evalResult{state: stateUnknown}
		}
		return evalResult{state: stateUnknown}

	case *ast.CompositeLit:
		return evalResult{state: stateNonNil}

	case *ast.StarExpr:
		// *p is a value, not dereferenceable itself for nil checks; model as
		// unknown. Dereference of p itself is handled as a statement/expr.
		return evalResult{state: stateUnknown}

	case *ast.IndexExpr:
		// m[k] without comma-ok yields a usable value of unknown nilness.
		return evalResult{state: stateUnknown}

	case *ast.SelectorExpr:
		// Qualified builtin/value identifiers (pkg.Var): unknown.
		if e.fn.pkg.Info.Selections[t] == nil {
			return evalResult{state: stateUnknown}
		}
		// A field or method selection; for nilness of fields we could track
		// fields, but fields themselves are usually value types. Pointer
		// fields are tracked indirectly via vars. Keep sound-unknown.
		return evalResult{state: stateUnknown}

	case *ast.TypeAssertExpr:
		// x.(T) without ok: panic if wrong type, but result can still be nil
		// when T is an interface. Treat as unknown (sound).
		return evalResult{state: stateUnknown}

	case *ast.CallExpr:
		return e.evalCall(t)

	case *ast.BinaryExpr:
		// "ptr == nil" itself is a boolean, not reference-typed.
		return evalResult{state: stateUnknown}

	case *ast.FuncLit:
		return evalResult{state: stateNonNil}
	}
	return evalResult{state: stateUnknown}
}
