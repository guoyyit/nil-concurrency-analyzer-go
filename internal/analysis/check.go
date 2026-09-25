package analysis

import (
	"go/ast"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// checkExpr inspects an expression for nil dereferences and records
// diagnostics. It does not mutate the environment.
func (r *runner) checkExpr(x ast.Expr, e *env) {
	if x == nil {
		return
	}
	switch t := x.(type) {
	case *ast.StarExpr:
		// *p
		r.checkExpr(t.X, e)
		r.derefIfPointer(t, t.X, e, "dereference")

	case *ast.SelectorExpr:
		// p.Field / p.Method(): a method call is visited via CallExpr.
		if sel := r.fn.pkg.Info.Selections[t]; sel != nil {
			if sel.Kind() == types.FieldVal {
				r.checkExpr(t.X, e)
				r.derefFieldIfPointer(t, e)
			}
		}

	case *ast.IndexExpr:
		r.checkExpr(t.Index, e)
		r.checkExpr(t.X, e)

	case *ast.CallExpr:
		r.checkCall(t, e, false)

	case *ast.UnaryExpr:
		r.checkExpr(t.X, e)

	case *ast.BinaryExpr:
		r.checkExpr(t.X, e)
		r.checkExpr(t.Y, e)

	case *ast.ParenExpr:
		r.checkExpr(t.X, e)

	case *ast.CompositeLit:
		for _, el := range t.Elts {
			r.checkExpr(el, e)
		}

	case *ast.KeyValueExpr:
		r.checkExpr(t.Key, e)
		r.checkExpr(t.Value, e)

	case *ast.FuncLit:
		// Closure body executes later/elsewhere; do not walk it with the
		// captured flow environment. It gets analyzed as its own anonymous
		// function (closures are handled separately).
		return

	case *ast.TypeAssertExpr:
		r.checkExpr(t.X, e)

	}
}

// derefIfPointer reports at the star expression when x is a pointer/interface
// that may be nil.
func (r *runner) derefIfPointer(node ast.Node, x ast.Expr, e *env, kind string) {
	tv := r.fn.pkg.Info.Types[x]
	pt, _ := tv.Type.(*types.Pointer)
	it, isIface := tv.Type.(*types.Interface)
	if pt == nil && !isIface {
		return
	}
	_ = it
	st := e.eval(x).state
	if st.mayBeNil() {
		r.reportDeref(node, x, st, e, kind)
		return
	}
	if r.pg.cfg.ReportGuarded && e.guardedExpr(x) != nil {
		r.reportGuardedDeref(node, kind)
	}
}

// derefFieldIfPointer reports implicit dereference in p.Field where p is a
// pointer and may be nil.
func (r *runner) derefFieldIfPointer(sel *ast.SelectorExpr, e *env) {
	tv := r.fn.pkg.Info.Types[sel.X]
	if _, ok := tv.Type.(*types.Pointer); !ok {
		return
	}
	st := e.eval(sel.X).state
	if st.mayBeNil() {
		r.reportDeref(sel, sel.X, st, e, "field access")
		return
	}
	if r.pg.cfg.ReportGuarded && e.guardedExpr(sel.X) != nil {
		r.reportGuardedDeref(sel, "field access")
	}
}

// reportGuardedDeref emits an audit-level "guarded" conclusion.
func (r *runner) reportGuardedDeref(node ast.Node, kind string) {
	r.diags = append(r.diags, &Diagnostic{
		Category:   config.NilDereference,
		Confidence: config.Guarded,
		Pos:        loader.PosOf(r.pg.fset, node.Pos()),
		Message:    kind + " is excluded by an explicit non-nil guard on this path",
	})
}

// reportDeref emits a nil-dereference diagnostic with confidence by state.
func (r *runner) reportDeref(node ast.Node, x ast.Expr, st nilState, e *env, kind string) {
	conf := config.Confidence("possible")
	if st.isNil() {
		conf = config.Confidence("certain")
	}
	pos := loader.PosOf(r.pg.fset, node.Pos())
	d := &Diagnostic{
		Category:   config.NilDereference,
		Confidence: conf,
		Pos:        pos,
		Message:    kind + " of a value that is " + st.String() + " on this path",
	}
	r.diags = append(r.diags, d)
}
