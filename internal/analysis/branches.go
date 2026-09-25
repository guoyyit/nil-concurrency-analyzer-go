package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// decl handles var declarations.
func (r *runner) decl(s *ast.DeclStmt, e *env) *env {
	gd, ok := s.Decl.(*ast.GenDecl)
	if !ok {
		return e
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		// Each name binds to the matching value expression.
		for i, name := range vs.Names {
			v, _ := r.fn.pkg.Info.Defs[name].(*types.Var)
			if v == nil {
				continue
			}
			var st nilState = stateUnknown
			if i < len(vs.Values) {
				val := vs.Values[i]
				r.checkExpr(val, e)
				st = e.eval(val).state
			} else {
				st = stateNil // var x *T => nil zero value
				if !isPointerish(v.Type()) {
					st = stateUnknown
				}
			}
			e.set(v, st)
		}
		// Single RHS spread: var a, b = f()
		if len(vs.Names) > 1 && len(vs.Values) == 1 {
			if call, ok := vs.Values[0].(*ast.CallExpr); ok {
				n := r.callResultCount(call, e)
				if n == len(vs.Names) {
					for i, name := range vs.Names {
						v, _ := r.fn.pkg.Info.Defs[name].(*types.Var)
						if v == nil {
							continue
						}
						e.set(v, r.callResult(call, i, e))
					}
				}
			}
		}
	}
	return e
}

// condRefinement is the state change imposed by a condition being true or
// false. Both environments fork from e.
func (r *runner) condEnvs(cond ast.Expr, e *env, polarity bool) *env {
	out := e.fork()
	r.refine(cond, polarity, out)
	return out
}

// refine applies nil/error comparisons to e for the branch where cond equals
// polarity.
func (r *runner) refine(cond ast.Expr, polarity bool, e *env) {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		r.refine(c.X, polarity, e)

	case *ast.BinaryExpr:
		switch c.Op {
		case token.LAND:
			if polarity {
				r.refine(c.X, true, e)
				r.refine(c.Y, true, e)
			} else {
				// !(A&&B) = !A||!B: join both refinements.
				a := e.fork()
				r.refine(c.X, false, a)
				b := e.fork()
				r.refine(c.Y, false, b)
				r.copyJoin(e, []*env{a, b})
			}
		case token.LOR:
			if polarity {
				a := e.fork()
				r.refine(c.X, true, a)
				b := e.fork()
				r.refine(c.Y, true, b)
				r.copyJoin(e, []*env{a, b})
			} else {
				r.refine(c.X, false, e)
				r.refine(c.Y, false, e)
			}
		case token.EQL, token.NEQ:
			r.refineCmp(c, polarity, e)
		}

	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			// !ok inverts the condition polarity; the inner ident must be
			// interpreted with that inverted polarity.
			r.refineBoolIndicator(c.X, !polarity, e)
		}

	case *ast.Ident:
		// if err { ... } style truthiness of an indicator variable.
		if c.Name == "nil" {
			return
		}
		if v := e.varOf(c); v != nil {
			if rel, ok := r.relFor(v); ok {
				r.refineIndicator(rel, polarity, e)
			}
		}
	}
}

// refineBoolIndicator handles a bare boolean indicator ident (possibly under
// a unary !) used directly as an if/for condition.
func (r *runner) refineBoolIndicator(x ast.Expr, polarity bool, e *env) {
	switch c := x.(type) {
	case *ast.ParenExpr:
		r.refineBoolIndicator(c.X, polarity, e)
	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			r.refineBoolIndicator(c.X, !polarity, e)
		}
	case *ast.Ident:
		if v := e.varOf(c); v != nil {
			if rel, ok := r.relFor(v); ok {
				r.refineIndicator(rel, polarity, e)
			}
		}
	}
}

// refineCmp handles x == nil / x != nil (and reversed) plus err comparisons.
func (r *runner) refineCmp(c *ast.BinaryExpr, polarity bool, e *env) {
	x, _ := nilSide(c)
	if x == nil {
		return
	}
	eq := c.Op == token.EQL
	// polarity == true means branch taken when comparison true.
	xNil := eq == polarity
	r.refineExprNilness(x, xNil, e)
}

// nilSide returns the non-nil operand if one side of a comparison is nil.
func nilSide(c *ast.BinaryExpr) (ast.Expr, bool) {
	if isNilLit(c.X) {
		return c.Y, true
	}
	if isNilLit(c.Y) {
		return c.X, false
	}
	return nil, false
}

func isNilLit(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "nil"
}

// refineExprNilness sets the state of reference-typed variables appearing in
// x to Nil/NonNil. Covers identifiers, parens and (best-effort) selectors.
func (r *runner) refineExprNilness(x ast.Expr, isNil bool, e *env) {
	switch t := x.(type) {
	case *ast.ParenExpr:
		r.refineExprNilness(t.X, isNil, e)
	case *ast.Ident:
		if v := e.varOf(t); v != nil && isPointerish(v.Type()) {
			e.set(v, boolState(isNil))
			if !isNil {
				e.markGuarded(v)
			}
			if rel, ok := r.relFor(v); ok {
				r.refineIndicator(rel, isIndicatorNil(rel, isNil), e)
			}
		}
	}
}

func boolState(isNil bool) nilState {
	if isNil {
		return stateNil
	}
	return stateNonNil
}

// isIndicatorNil maps "the indicator variable equals nil" to a success flag:
// error indicator nil means success; bool indicator has no nil comparison.
func isIndicatorNil(rel *rel, isNil bool) bool {
	// err == nil means success; a boolean indicator is never compared to
	// nil, so the same mapping applies.
	return isNil
}

// refineIndicator refines companion values according to success/failure.
func (r *runner) refineIndicator(rel *rel, success bool, e *env) {
	n := r.callResultCount(rel.call, e)
	ok2, _, succ, fail := r.indicatorSplit(rel.call, rel.idx, n)
	if !ok2 {
		return
	}
	if success {
		e.set(rel.val, succ)
	} else {
		e.set(rel.val, fail)
	}
}

// copyJoin overwrites dst with the pointwise merge of branches.
func (r *runner) copyJoin(dst *env, branches []*env) {
	merged := mergeEnvs(branches)
	if merged == nil {
		return
	}
	for k := range dst.m {
		delete(dst.m, k)
	}
	for k, v := range merged.m {
		dst.m[k] = v
	}
}

// ifStmt processes init, condition refinement, then branches.
func (r *runner) ifStmt(s *ast.IfStmt, e *env) *env {
	if s.Init != nil {
		e = r.stmt(s.Init, e.fork())
		if e == nil {
			return nil
		}
	}
	r.checkCond(s.Cond, e)
	thenE := r.condEnvs(s.Cond, e, true)
	elseE := r.condEnvs(s.Cond, e, false)
	thenOut := r.stmts(s.Body.List, thenE)
	var elseOut *env
	if s.Else != nil {
		elseOut = r.stmt(s.Else, elseE)
	} else {
		elseOut = elseE
	}
	return joinOrNil(thenOut, elseOut)
}

// checkCond looks for nil-deref within a condition expression without
// changing env.
func (r *runner) checkCond(cond ast.Expr, e *env) {
	ast.Inspect(cond, func(n ast.Node) bool {
		if x, ok := n.(ast.Expr); ok {
			r.checkExpr(x, e)
		}
		return true
	})
}

func joinOrNil(a, b *env) *env {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return mergeEnvs([]*env{a, b})
}

// forStmt widens at the loop head (fixed number of iterations is unnecessary
// for the nil lattice: one merge reaches a fixed point).
func (r *runner) forStmt(s *ast.ForStmt, e *env) *env {
	if s.Init != nil {
		e = r.stmt(s.Init, e)
	}
	// Entry state into condition.
	head := e
	if s.Cond != nil {
		r.checkCond(s.Cond, head)
	}
	body := head.fork()
	if s.Cond != nil {
		r.refine(s.Cond, true, body)
	}
	bodyOut := r.stmts(s.Body.List, body)
	if bodyOut != nil && s.Post != nil {
		bodyOut = r.stmt(s.Post, bodyOut)
	}
	// Merge back to head and run body once more from the merged state.
	if bodyOut != nil {
		head2 := mergeEnvs([]*env{head, bodyOut})
		if s.Cond != nil {
			r.checkCond(s.Cond, head2)
		}
		body2 := head2.fork()
		if s.Cond != nil {
			r.refine(s.Cond, true, body2)
		}
		out := r.stmts(s.Body.List, body2)
		if out != nil && s.Post != nil {
			out = r.stmt(s.Post, out)
		}
		// Exit environment: condition false under the fixed-point head.
		exit := head2.fork()
		if s.Cond != nil {
			r.refine(s.Cond, false, exit)
		}
		_ = out
		return exit
	}
	exit := head.fork()
	if s.Cond != nil {
		r.refine(s.Cond, false, exit)
	}
	return exit
}

// rangeStmt checks the iterable and treats the body conservatively.
func (r *runner) rangeStmt(s *ast.RangeStmt, e *env) *env {
	r.checkExpr(s.X, e)
	if s.Tok == token.DEFINE {
		if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
			if v := e.varOf(id); v != nil {
				e.set(v, stateUnknown)
			}
		}
		if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
			if v := e.varOf(id); v != nil {
				e.set(v, stateUnknown)
			}
		}
	}
	body := e.fork()
	out := r.stmts(s.Body.List, body)
	return joinOrNil(e, out)
}

// switchStmt handles expression switches with simple case lists.
func (r *runner) switchStmt(s *ast.SwitchStmt, e *env) *env {
	if s.Init != nil {
		e = r.stmt(s.Init, e)
	}
	if s.Tag != nil {
		r.checkExpr(s.Tag, e)
	}
	var outs []*env
	fall := e
	for _, cc := range s.Body.List {
		clause := cc.(*ast.CaseClause)
		br := fall.fork()
		// Tag-based equality against nil is uncommon; only guard truthiness
		// relations on the tag if it is a bare indicator.
		if s.Tag == nil {
			for _, cond := range clause.List {
				r.checkCond(cond, br)
			}
		}
		out := r.stmts(clause.Body, br)
		if out != nil {
			outs = append(outs, out)
		}
		// Without fallthrough analysis each case starts from the switch entry.
	}
	// Default (or no case matched) path keeps entry env.
	outs = append(outs, e)
	return mergeEnvs(outs)
}

// typeSwitchStmt checks the asserted value and runs each case; variables
// introduced by := are conservatively unknown.
func (r *runner) typeSwitchStmt(s *ast.TypeSwitchStmt, e *env) *env {
	if s.Init != nil {
		e = r.stmt(s.Init, e)
	}
	var outs []*env
	for _, cc := range s.Body.List {
		clause := cc.(*ast.CaseClause)
		br := e.fork()
		out := r.stmts(clause.Body, br)
		if out != nil {
			outs = append(outs, out)
		}
	}
	outs = append(outs, e)
	return mergeEnvs(outs)
}

// selectStmt models a select over comms: check channel exprs and join bodies.
func (r *runner) selectStmt(s *ast.SelectStmt, e *env) *env {
	var outs []*env
	for _, cc := range s.Body.List {
		clause := cc.(*ast.CommClause)
		br := e.fork()
		if clause.Comm != nil {
			if send, ok := clause.Comm.(*ast.SendStmt); ok {
				r.checkExpr(send.Value, br)
			} else if as, ok := clause.Comm.(*ast.AssignStmt); ok {
				r.stmt(as, br)
			}
		}
		out := r.stmts(clause.Body, br)
		if out != nil {
			outs = append(outs, out)
		}
	}
	outs = append(outs, e)
	return mergeEnvs(outs)
}

var _ types.Object
