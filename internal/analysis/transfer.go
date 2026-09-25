package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// runner carries per-function analysis state.
type runner struct {
	pg *Program
	fn *funcDecl
	// callProvenance maps a synthetic result var to its originating call.
	provenance map[*types.Var]*ast.CallExpr
	// diagnostics emitted during this run.
	diags []*Diagnostic
	// exits collected for summary computation.
	exits []exitRecord
	// varRels groups companion relations per indicator variable.
	varRels map[*types.Var][]*rel
	// paramEntry states for this run, keyed by parameter var.
	paramEntry map[*types.Var]nilState
	// callArgs records actual-argument states at each static call (in order).
	callArgs []callArgs
	// depth bounds recursion through nested function bodies.
	depth int
}

func (p *Program) newRunner(fn *funcDecl) *runner {
	return &runner{pg: p, fn: fn, provenance: map[*types.Var]*ast.CallExpr{}}
}

// stmts executes a statement list threading the environment.
func (r *runner) stmts(list []ast.Stmt, e *env) *env {
	for _, st := range list {
		if e == nil {
			return nil
		}
		e = r.stmt(st, e)
	}
	return e
}

// stmt executes one statement and returns the join environment at its end.
func (r *runner) stmt(st ast.Stmt, e *env) *env {
	switch s := st.(type) {
	case *ast.BlockStmt:
		return r.stmts(s.List, e.fork())

	case *ast.AssignStmt:
		return r.assign(s, e)

	case *ast.DeclStmt:
		return r.decl(s, e)

	case *ast.IfStmt:
		return r.ifStmt(s, e)

	case *ast.ForStmt:
		return r.forStmt(s, e)

	case *ast.RangeStmt:
		return r.rangeStmt(s, e)

	case *ast.SwitchStmt:
		return r.switchStmt(s, e)

	case *ast.TypeSwitchStmt:
		return r.typeSwitchStmt(s, e)

	case *ast.ReturnStmt:
		return r.returnStmt(s, e)

	case *ast.ExprStmt:
		r.checkExpr(s.X, e)
		return e

	case *ast.IncDecStmt:
		r.checkExpr(s.X, e)
		return e

	case *ast.BranchStmt, *ast.EmptyStmt:
		return e

	case *ast.LabeledStmt:
		return r.stmt(s.Stmt, e)

	case *ast.SelectStmt:
		return r.selectStmt(s, e)

	case *ast.GoStmt:
		r.checkCall(s.Call, e, false)
		return e

	case *ast.DeferStmt:
		r.checkCall(s.Call, e, false)
		return e

	case *ast.SendStmt:
		// nil-channel send is handled by concurrency analysis; check value expr.
		r.checkExpr(s.Value, e)
		return e

	default:
		// Conservative: inspect expressions for dereferences but keep env.
		ast.Inspect(st, func(n ast.Node) bool {
			if x, ok := n.(ast.Expr); ok {
				r.checkExpr(x, e)
			}
			return true
		})
		return e
	}
}

// assign handles =, := (including multi-result calls and comma-ok).
func (r *runner) assign(s *ast.AssignStmt, e *env) *env {
	// Evaluate RHS values in the current environment.
	lhs := s.Lhs
	rhs := s.Rhs

	// x, ok := m[k] / x.(T) / <-ch
	if len(lhs) == 2 && len(rhs) == 1 {
		if states := r.evalOkPattern(rhs[0], e); states != nil {
			r.bindLHS(lhs[0], states[0], s.Tok, e)
			if id, ok := lhs[1].(*ast.Ident); ok && id.Name != "_" {
				if v := e.varOf(id); v != nil {
					e.set(v, states[1])
				}
			}
			return e
		}
	}

	// Single RHS call with N results and N LHS.
	if len(lhs) > 1 && len(rhs) == 1 {
		if call, ok := rhs[0].(*ast.CallExpr); ok {
			n := r.callResultCount(call, e)
			if n == len(lhs) {
				r.checkCall(call, e, false)
				for i := range lhs {
					st := r.callResult(call, i, e)
					r.bindLHS(lhs[i], st, s.Tok, e)
				}
				r.registerRels(call, n, lhs, e)
				return e
			}
		}
	}
	// General: broadcast single RHS or pair-wise.
	for i, lx := range lhs {
		var val ast.Expr
		if len(rhs) == 1 {
			val = rhs[0]
		} else if i < len(rhs) {
			val = rhs[i]
		}
		r.checkExpr(val, e)
		st := r.evalForAssign(lx, val, e)
		r.bindLHS(lx, st, s.Tok, e)
	}
	return e
}

// evalForAssign evaluates RHS with extra LHS context (pointer fields).
func (r *runner) evalForAssign(lx, rx ast.Expr, e *env) nilState {
	if rx == nil {
		return stateUnknown
	}
	return e.eval(rx).state
}

// bindLHS binds an lvalue: identifier or selector/index chains.
func (r *runner) bindLHS(lx ast.Expr, st nilState, tok token.Token, e *env) *types.Var {
	switch t := lx.(type) {
	case *ast.Ident:
		if t.Name == "_" {
			return nil
		}
		v := e.varOf(t)
		if v == nil {
			return nil
		}
		if tok == token.DEFINE {
			e.set(v, st)
		} else {
			e.set(v, merge(e.get(v), st))
		}
		return v
	case *ast.SelectorExpr, *ast.IndexExpr:
		// Field/index updates are not modeled precisely; the RHS is still
		// checked. This keeps soundness for later reads (unknown) and avoids
		// false positives on aliasing.
		return nil
	}
	return nil
}

// okPattern holds (value, ok) states.
func (r *runner) evalOkPattern(x ast.Expr, e *env) []nilState {
	switch t := x.(type) {
	case *ast.IndexExpr:
		// v, ok := m[k]
		r.checkExpr(t.Index, e)
		// value is usable when ok; unknown overall -> we keep value may-nil
		// because reading a zero element is allowed even on success.
		return []nilState{stateUnknown, stateNonNil}
	case *ast.TypeAssertExpr:
		r.checkExpr(t.X, e)
		// x.(T): on success the asserted value's nilness follows x; but a
		// concrete-typed assertion yields non-nil interface.
		return []nilState{stateUnknown, stateNonNil}
	case *ast.UnaryExpr:
		if t.Op == token.ARROW {
			r.checkExpr(t.X, e)
			return []nilState{stateUnknown, stateNonNil}
		}
	}
	return nil
}

// callResultCount returns the number of values a call yields in the given
// position (single vs multi-result context).
func (r *runner) callResultCount(call *ast.CallExpr, e *env) int {
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil {
		return len(callee.results)
	}
	if typ := r.fn.pkg.Info.TypeOf(call); typ != nil {
		if tup, ok := typ.(*types.Tuple); ok {
			return tup.Len()
		}
	}
	return 1
}
