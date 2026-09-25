package analysis

import (
	"go/types"
)

// runFunction performs one intraprocedural pass with given entry states for
// the parameters (nil entry means all Unknown). It returns emitted
// diagnostics and collected exits.
func (p *Program) runFunction(fn *funcDecl, entry map[*types.Var]nilState) ([]*Diagnostic, []exitRecord) {
	r := p.newRunner(fn)
	r.varRels = map[*types.Var][]*rel{}
	r.paramEntry = entry
	r.callArgs = nil
	r.callArgs = nil
	e := p.newEnv(fn)
	p.initParams(fn, entry, e)
	out := r.stmts(fn.decl.Body.List, e)
	_ = out
	return r.diags, r.exits
}

// runFunctionSummaries returns (diagnostics, exits) and also keeps relation
// state via a reusable runner (used by the nil-analysis driver).
func (p *Program) runWithRunner(fn *funcDecl, entry map[*types.Var]nilState) *runner {
	r := p.newRunner(fn)
	r.varRels = map[*types.Var][]*rel{}
	r.paramEntry = entry
	r.callArgs = nil
	r.callArgs = nil
	e := p.newEnv(fn)
	p.initParams(fn, entry, e)
	r.stmts(fn.decl.Body.List, e)
	return r
}

var _ types.Type

// initParams seeds parameter states. Pointer/interface method receivers are
// non-nil by construction inside a method body (a nil receiver call is
// reported at the callsite instead). Ordinary pointer parameters start as
// Unknown and are refined by cross-function entry propagation.
func (p *Program) initParams(fn *funcDecl, entry map[*types.Var]nilState, e *env) {
	recv := fn.sig.Recv()
	for _, v := range fn.params {
		if recv != nil && v == recv {
			switch underlying(v.Type()).(type) {
			case *types.Pointer, *types.Interface:
				if _, ok := entry[v]; !ok {
					e.set(v, stateNonNil)
					continue
				}
			}
		}
		if st, ok := entry[v]; ok {
			e.set(v, st)
		}
	}
}
