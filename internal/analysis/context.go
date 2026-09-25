package analysis

import (
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// callContext is one distinct argument-state vector observed at callsites of
// a function, together with a representative callsite position.
type callContext struct {
	states map[*types.Var]nilState
	site   loader.FilePos
}

// collectContexts gathers, for every function, the distinct argument-state
// vectors observed at its callsites during a final propagation run.
func (p *Program) collectContexts(order []*funcDecl) map[*funcDecl][]callContext {
	out := map[*funcDecl][]callContext{}
	seen := map[*funcDecl]map[string]bool{}
	for _, fd := range order {
		r := p.runWithRunner(fd, nil)
		for _, ca := range r.callArgs {
			params := ca.callee.sig.Params()
			if params.Len() != len(ca.states) {
				continue
			}
			key := ""
			entry := map[*types.Var]nilState{}
			for i, st := range ca.states {
				entry[params.At(i)] = st
				key += st.String() + "|"
			}
			m := seen[ca.callee]
			if m == nil {
				m = map[string]bool{}
				seen[ca.callee] = m
			}
			if m[key] {
				continue
			}
			m[key] = true
			out[ca.callee] = append(out[ca.callee], callContext{
				states: entry,
				site:   loader.PosOf(p.fset, ca.call.Pos()),
			})
		}
	}
	return out
}

// contextDiagnostics runs the function once per observed context and keeps
// diagnostics, attaching the callsite to reports that only fire under a
// nil/may-nil parameter context.
func (p *Program) contextDiagnostics(fd *funcDecl, ctxs []callContext) []*Diagnostic {
	if len(ctxs) == 0 {
		// No observed callsites: analyze with parameters Unknown. Derefs of
		// unknown params are not reported, so only locally-provable nils
		// surface.
		r := p.runWithRunner(fd, nil)
		return dedupDiags(r.diags)
	}
	var out []*Diagnostic
	seen := map[string]bool{}
	for _, ctx := range ctxs {
		r := p.runWithRunner(fd, ctx.states)
		for _, d := range dedupDiags(r.diags) {
			key := diagKey(d)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Attach the callsite that triggered this context for nil-deref
			// reports involving parameters.
			if d.Category == catNil && len(d.CallPath) == 0 {
				d.CallPath = []loader.FilePos{ctx.site}
			}
			out = append(out, d)
		}
	}
	return out
}

func diagKey(d *Diagnostic) string {
	return string(d.Category) + "|" + d.Pos.File + "|" + itoa2(d.Pos.Line) + "|" + itoa2(d.Pos.Column) + "|" + d.Message
}
