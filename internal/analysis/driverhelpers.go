package analysis

import (
	"go/ast"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

const catNil = config.NilDereference

func pPositionKey(fd *funcDecl) string {
	pos := programPosition(fd)
	return pos.File + ":" + itoa2(pos.Line)
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// pIndicatorSplitFor resolves an indicator split from a callee summary or
// external stub during summary construction.
func pIndicatorSplitFor(p *Program, fd *funcDecl, call *ast.CallExpr, idx, nResults int) (bool, indicatorKind, nilState, nilState) {
	_ = p
	if callee := p.calleeAt(fd.pkg, call); callee != nil {
		if callee.summary != nil {
			s := callee.summary
			if s.errIndex == nResults-1 && idx != s.errIndex {
				return true, indError, s.returns[idx], s.errorReturns[idx]
			}
			if s.boolIndex == nResults-1 && idx != s.boolIndex {
				return true, indBool, s.returns[idx], s.boolReturns[idx]
			}
		}
		return false, 0, stateUnknown, stateUnknown
	}
	ok2, kind, succ, fail := externalIndicatorSplit(fd, call, idx, nResults)
	return ok2, kind, succ, fail
}

// collectCalleeEntries merges argument states observed at callsites into
// per-callee parameter entry maps.
func collectCalleeEntries(r *runner) map[*funcDecl]map[*types.Var]nilState {
	out := map[*funcDecl]map[*types.Var]nilState{}
	for _, ca := range r.callArgs {
		params := ca.callee.sig.Params()
		if params.Len() != len(ca.states) {
			continue
		}
		em := out[ca.callee]
		if em == nil {
			em = map[*types.Var]nilState{}
			out[ca.callee] = em
		}
		for i, st := range ca.states {
			em[params.At(i)] = merge(em[params.At(i)], st)
		}
	}
	return out
}

func programPosition(fd *funcDecl) filePositionLike {
	if fd == nil || fd.pkg == nil || fd.pkg.Fset() == nil {
		return filePositionLike{}
	}
	p := fd.pkg.Fset().Position(fd.decl.Pos())
	return filePositionLike{File: p.Filename, Line: p.Line, Column: p.Column}
}

type filePositionLike struct {
	File   string
	Line   int
	Column int
}

// externalIndicatorSplit models (T, error/bool) splits for non-local calls
// without constructing a runner.
func externalIndicatorSplit(fd *funcDecl, call *ast.CallExpr, idx, nResults int) (bool, indicatorKind, nilState, nilState) {
	fn := externalFuncOf(fd.pkg, call)
	if fn == nil {
		return false, 0, stateUnknown, stateUnknown
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || idx >= sig.Results().Len()-1 {
		return false, 0, stateUnknown, stateUnknown
	}
	last := sig.Results().Len() - 1
	if isErrorType(sig.Results().At(last).Type()) {
		succ, fail := stubCallSplit(fn, idx)
		return true, indError, succ, fail
	}
	if b, ok := sig.Results().At(last).Type().(*types.Basic); ok && b.Kind() == types.Bool {
		succ, fail := stubCallSplit(fn, idx)
		return true, indBool, succ, fail
	}
	return false, 0, stateUnknown, stateUnknown
}

func externalFuncOf(pkg *loader.Package, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ := pkg.Info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel := pkg.Info.Selections[fun]; sel != nil && sel.Kind() == types.MethodVal {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := pkg.Info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}
