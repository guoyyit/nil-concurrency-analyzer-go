package analysis

import (
	"go/types"
	"sort"
)

const maxSCCRounds = 6

// analyzeNil computes cross-function summaries bottom-up, propagates
// caller argument nilness to callee parameters, and performs a final
// diagnostic pass.
func (p *Program) analyzeNil() {
	if !p.cfg.IsEnabled(catNil) {
		return
	}
	order := p.topoOrder()

	// Pass 1: summaries with parameters assumed Unknown (deps-first so each
	// callee summary is already at a stable value unless recursive).
	for _, fd := range order {
		if fd.summary == nil {
			fd.summary = newSummary(fd.sig)
		}
		r := p.runWithRunner(fd, nil)
		fd.summary = buildSummary(fd, p, r.exits)
	}

	// Pass 2: iterate over strongly connected regions for recursion and for
	// caller->parameter propagation, keeping each function's current summary.
	entries := map[*funcDecl]map[*types.Var]nilState{}
	for round := 0; round < maxSCCRounds; round++ {
		changed := false
		for _, fd := range order {
			entry := entries[fd]
			r := p.runWithRunner(fd, entry)
			newSum := buildSummary(fd, p, r.exits)
			if !summariesEqual(fd.summary, newSum) {
				fd.summary = newSum
				changed = true
			}
			// Propagate actual argument states at each callsite.
			next := collectCalleeEntries(r)
			for callee, em := range next {
				merged := mergeEntryMaps(entries[callee], em)
				if !entryMapsEqual(entries[callee], merged) {
					entries[callee] = merged
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	// Final diagnostic pass with one run per callsite argument-state vector
	// (context splitting on the finite nil lattice). This keeps a nil/may-nil
	// argument at one callsite from tainting a deref made from a guarded or
	// non-nil callsite.
	ctxByCallee := p.collectContexts(order)
	for _, fd := range order {
		fd.diags = appendNilDiags(fd.diags, p.contextDiagnostics(fd, ctxByCallee[fd]))
	}
}

// topoOrder returns functions in dependency-first order with deterministic
// tie-breaking; edges to functions outside the program are ignored.
func (p *Program) topoOrder() []*funcDecl {
	all := make([]*funcDecl, 0, len(p.funcs))
	for _, fd := range p.funcs {
		all = append(all, fd)
	}
	sort.Slice(all, func(i, j int) bool {
		return funcKey(all[i]) < funcKey(all[j])
	})
	tmp := map[*funcDecl]bool{}
	done := map[*funcDecl]bool{}
	var out []*funcDecl
	var visit func(fd *funcDecl)
	visit = func(fd *funcDecl) {
		switch {
		case done[fd]:
			return
		case tmp[fd]:
			return // back edge inside a cycle
		}
		tmp[fd] = true
		edges := append([]*funcDecl(nil), fd.edges...)
		sort.Slice(edges, func(i, j int) bool { return funcKey(edges[i]) < funcKey(edges[j]) })
		for _, e := range edges {
			visit(e)
		}
		tmp[fd] = false
		done[fd] = true
		out = append(out, fd)
	}
	for _, fd := range all {
		visit(fd)
	}
	return out
}

func funcKey(fd *funcDecl) string {
	pos := pPositionKey(fd)
	return fd.pkgPath + ":" + pos + ":" + fd.obj.Name()
}

// buildSummary merges exit records into a function summary, splitting
// error/failure exits from success exits using literal indicator values and
// direct indicator-call correlations.
func buildSummary(fd *funcDecl, p *Program, exits []exitRecord) *summary {
	sum := newSummary(fd.sig)
	n := len(fd.results)
	if n == 0 {
		return sum
	}
	var success, failure []exitRecord
	var neutral []exitRecord
	for _, ex := range exits {
		if kind, val, ok := exitIndicator(fd, ex); ok {
			if (kind == indError && !val) || (kind == indBool && val) {
				success = append(success, ex)
			} else {
				failure = append(failure, ex)
			}
		} else {
			neutral = append(neutral, ex)
		}
	}
	// "returns" describes a caller that ignores the indicator: join every
	// exit (success + failure + neutral). The error/bool-correlated splits
	// are kept separately in errorReturns / boolReturns.
	all := append(append(append([]exitRecord{}, success...), failure...), neutral...)
	for i := 0; i < n; i++ {
		s := stateBottom
		for _, ex := range all {
			if i < len(ex.states) {
				s = merge(s, ex.states[i])
			}
		}
		sum.returns[i] = s
		ss := stateBottom
		for _, ex := range success {
			if i < len(ex.states) {
				ss = merge(ss, ex.states[i])
			}
		}
		if sum.errIndex >= 0 || sum.boolIndex >= 0 {
			sum.successReturns[i] = ss
		} else {
			sum.successReturns[i] = s
		}
		fs := stateBottom
		for _, ex := range failure {
			if i < len(ex.states) {
				fs = merge(fs, ex.states[i])
			}
		}
		if sum.errIndex >= 0 {
			sum.errorReturns[i] = fs
		}
		if sum.boolIndex >= 0 {
			sum.boolReturns[i] = fs
		}
	}
	// Collect direct parameter pass-through ("return p").
	for _, ex := range exits {
		for i, pi := range ex.retParam {
			if pi >= 0 && i < len(sum.paramReturn) && sum.paramReturn[i] < 0 {
				sum.paramReturn[i] = pi
			}
		}
	}
	// When an exit is correlated to a callee indicator via pass-through
	// (return f()), override companion slots with callee split states.
	applyPassThrough(fd, p, exits, sum)
	return sum
}

// exitIndicator classifies an exit by a literal nil/non-nil error result or
// boolean literal, returning (kind, value, ok). For errors value means
// "is error non-nil"; for bools it means "is success".
func exitIndicator(fd *funcDecl, ex exitRecord) (indicatorKind, bool, bool) {
	if len(ex.states) == 0 {
		return 0, false, false
	}
	sum := newSummary(fd.sig)
	last := len(fd.results) - 1
	if sum.errIndex == last {
		if ex.errNil != nil {
			return indError, *ex.errNil, true
		}
		st := ex.states[last]
		switch st {
		case stateNil:
			return indError, false, true
		case stateNonNil:
			return indError, true, true
		}
	}
	if sum.boolIndex == last && ex.boolLiteral != nil {
		return indBool, *ex.boolLiteral, true
	}
	return 0, false, false
}

// applyPassThrough handles `return callee(...)` by pulling the callee's
// success/failure split into this function's summary for companions.
func applyPassThrough(fd *funcDecl, p *Program, exits []exitRecord, sum *summary) {
	for _, ex := range exits {
		for _, er := range ex.rels {
			if er.rel == nil || er.rel.call == nil {
				continue
			}
			// The companion result state on this exit follows the callee's
			// split, intersected with whichever side this return sits on.
			n := len(ex.states)
			ok, kind, succ, fail := pIndicatorSplitFor(p, fd, er.rel.call, er.rel.idx, n)
			if !ok {
				continue
			}
			_ = kind
			// Pass-through ("return f(...)"): this exit can land on either
			// side of f's indicator, so propagate both split states.
			if kind == indError {
				sum.successReturns[er.idx] = merge(sum.successReturns[er.idx], succ)
				sum.errorReturns[er.idx] = merge(sum.errorReturns[er.idx], fail)
			} else if kind == indBool {
				sum.successReturns[er.idx] = merge(sum.successReturns[er.idx], succ)
				sum.boolReturns[er.idx] = merge(sum.boolReturns[er.idx], fail)
			}
			// If this return is itself classified by a literal indicator,
			// constrain to that side only.
			if k, val, isCl := exitIndicator(fd, ex); isCl {
				if (k == indError && !val) || (k == indBool && val) {
					// success-only return
				}
			}
		}
	}
}

// summariesEqual compares two summaries on the lattice.
func summariesEqual(a, b *summary) bool {
	if a == nil || b == nil {
		return a == b
	}
	return stateSlicesEqual(a.returns, b.returns) &&
		stateSlicesEqual(a.errorReturns, b.errorReturns) &&
		stateSlicesEqual(a.boolReturns, b.boolReturns) &&
		stateSlicesEqual(a.successReturns, b.successReturns) &&
		intsEqual(a.paramReturn, b.paramReturn)
}

func stateSlicesEqual(a, b []nilState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mergeEntryMaps(a, b map[*types.Var]nilState) map[*types.Var]nilState {
	if a == nil && b == nil {
		return nil
	}
	out := map[*types.Var]nilState{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = merge(out[k], v)
	}
	return out
}

func entryMapsEqual(a, b map[*types.Var]nilState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func appendNilDiags(dst, src []*Diagnostic) []*Diagnostic {
	return append(dst, src...)
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
