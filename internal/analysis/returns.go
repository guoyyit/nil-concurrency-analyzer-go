package analysis

import (
	"go/ast"
	"go/types"
)

// returnStmt records the nil states of returned expressions in the runner for
// summary computation, then returns nil (no fallthrough after return).
func (r *runner) returnStmt(s *ast.ReturnStmt, e *env) *env {
	results := r.fn.results
	rec := exitRecord{states: make([]nilState, len(results))}

	defer func() { r.exits = append(r.exits, rec) }()

	if len(s.Results) == 0 {
		// Naked return: named results carry the current env states.
		for i, v := range results {
			rec.states[i] = e.get(v)
		}
		return nil
	}

	if len(s.Results) == 1 && len(results) > 1 {
		// return f() with a multi-result call.
		if call, ok := s.Results[0].(*ast.CallExpr); ok {
			n := r.callResultCount(call, e)
			if n == len(results) {
				for i := 0; i < n; i++ {
					rec.states[i] = r.callResult(call, i, e)
				}
				rec = r.classifyExit(rec.states, call, e)
				return nil
			}
		}
		// Single expression returning a tuple via other forms: evaluate it
		// for every result slot best-effort.
		for i := range results {
			rec.states[i] = e.eval(s.Results[0]).state
		}
		r.exits = append(r.exits, rec)
		return nil
	}

	for i, rx := range s.Results {
		r.checkExpr(rx, e)
		if i < len(results) {
			rec.states[i] = e.eval(rx).state
		}
	}
	// Track direct indicator variable pass-through and literal indicators.
	if len(s.Results) == len(results) {
		for i, rx := range s.Results {
			if id, ok := rx.(*ast.Ident); ok {
				if v := e.varOf(id); v != nil {
					if rel, ok := r.relFor(v); ok {
						rec.rels = append(rec.rels, exitRel{idx: i, rel: rel})
					}
				}
			}
		}
	}
	r.markIndicatorLiteral(&rec, s.Results)
	return nil
}

// markIndicatorLiteral fills errNil/boolLiteral when the final returned
// expression is a nil / true / false literal.
func (r *runner) markIndicatorLiteral(rec *exitRecord, exprs []ast.Expr) {
	if len(r.fn.results) == 0 || len(exprs) != len(r.fn.results) {
		return
	}
	last := len(exprs) - 1
	sum := newSummary(r.fn.sig)
	lit := exprs[last]
	if id, ok := lit.(*ast.Ident); ok {
		if sum.errIndex == last && id.Name == "nil" {
			b := false
			rec.errNil = &b
		}
		if sum.boolIndex == last {
			if v, ok := boolLiteralValue(id); ok {
				rec.boolLiteral = &v
			}
		}
	}
}

// exitRecord is one return site with per-result nil states.
type exitRecord struct {
	states      []nilState
	rels        []exitRel
	errNil      *bool // explicit nil/non-nil error literal
	boolLiteral *bool // explicit true/false literal
	// retParam[i] is a parameter index when result i is returned directly.
	retParam []int
}

type exitRel struct {
	idx int
	rel *rel
}

// classifyExit builds a record for "return f()" and tags the exit by the
// error/bool outcome whenever f's indicator is directly returned.
func (r *runner) classifyExit(states []nilState, call *ast.CallExpr, e *env) exitRecord {
	rec := exitRecord{states: states}
	n := len(states)
	last := n - 1
	if callee := r.pg.calleeAt(r.fn.pkg, call); callee != nil && callee.summary != nil {
		s := callee.summary
		if s.errIndex == last || s.boolIndex == last {
			for i := 0; i < last; i++ {
				kind := indError
				if s.boolIndex == last {
					kind = indBool
				}
				rec.rels = append(rec.rels, exitRel{
					idx: i,
					rel: &rel{call: call, idx: i, indicator: last, kind: kind},
				})
			}
		}
	}
	if fn := r.externalFunc(call); fn != nil {
		if sig, ok := fn.Type().(*types.Signature); ok {
			if last == sig.Results().Len()-1 && last >= 0 {
				t := sig.Results().At(last).Type()
				if isErrorType(t) {
					for i := 0; i < last; i++ {
						rec.rels = append(rec.rels, exitRel{
							idx: i,
							rel: &rel{call: call, idx: i, indicator: last, kind: indError},
						})
					}
				}
			}
		}
	}
	return rec
}

// relFor finds a recorded indicator relation for an env variable.
func (r *runner) relFor(v *types.Var) (*rel, bool) {
	// v is the indicator variable (map key).
	if rels, ok := r.varRels[v]; ok && len(rels) > 0 {
		return rels[0], true
	}
	// Also support direct companion lookup.
	for _, rels := range r.varRels {
		for _, rl := range rels {
			if rl.val == v {
				return rl, true
			}
		}
	}
	return nil, false
}

// boolValue reports the value of a true/false literal ident.
func boolLiteralValue(id *ast.Ident) (bool, bool) {
	switch id.Name {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}
