package analysis

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// checkCopylocks implements the "copied lock" check, modeled after go vet's
// copylocks pass: values containing Mutex/RWMutex/WaitGroup/Once (or types
// embedding them) must not be copied.
func (p *Program) checkCopylocks() {
	if !p.cfg.IsEnabled(config.CopiedLock) {
		return
	}
	for _, fd := range p.funcs {
		// By-value parameters.
		for _, v := range fd.params {
			if info, ok := containsLock(v.Type()); ok {
				conf := config.Certain
				if info.kind == lockNone || !isStdLock(info) {
					conf = config.Suspect
				}
				fd.diags = append(fd.diags, &Diagnostic{
					Category:   config.CopiedLock,
					Confidence: conf,
					Pos:        loader.PosOf(p.fset, fd.decl.Pos()),
					Message:    "parameter " + v.Name() + " passes " + info.name + " by value; locks must not be copied (use a pointer)",
				})
			}
		}
		// By-value named results.
		for _, v := range fd.results {
			if info, ok := containsLock(v.Type()); ok {
				fd.diags = append(fd.diags, &Diagnostic{
					Category:   config.CopiedLock,
					Confidence: confidenceFor(info),
					Pos:        loader.PosOf(p.fset, fd.decl.Pos()),
					Message:    "result " + v.Name() + " returns " + info.name + " by value; locks must not be copied",
				})
			}
		}
		if fd.decl.Body == nil {
			continue
		}
		ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				p.inspectCopylockAssign(fd, node)
			case *ast.CallExpr:
				p.inspectCopylockCall(fd, node)
			case *ast.RangeStmt:
				p.inspectCopylockRange(fd, node)
			case *ast.ReturnStmt:
				p.inspectCopylockReturn(fd, node)
			case *ast.SendStmt:
				p.inspectCopylockSend(fd, node)
			}
			return true
		})
	}
}

func isStdLock(info lockInfo) bool {
	switch info.kind {
	case lockMutex, lockRWMutex, lockWaitGroup, lockOnce, lockAtomic, lockCond:
		return true
	}
	return false
}

func confidenceFor(info lockInfo) config.Confidence {
	if isStdLock(info) {
		return config.Certain
	}
	return config.Suspect
}

// inspectCopylockAssign flags "dst := src" where src carries a lock.
func (p *Program) inspectCopylockAssign(fd *funcDecl, s *ast.AssignStmt) {
	// range-clause assignments are handled by inspectCopylockRange.
	for _, rx := range s.Rhs {
		// Composite literals construct a new value; copying happens when it is
		// subsequently moved. &T{} is a pointer and safe.
		if _, ok := rx.(*ast.CompositeLit); ok {
			continue
		}
		if _, ok := rx.(*ast.UnaryExpr); ok {
			continue // &x
		}
		t := fd.pkg.Info.TypeOf(rx)
		if t == nil {
			continue
		}
		if info, ok := containsLock(t); ok {
			p.reportCopy(fd, rx.Pos(), info, "assignment copies a value containing "+info.name)
		}
	}
}

// inspectCopylockCall flags by-value arguments copying locks.
func (p *Program) inspectCopylockCall(fd *funcDecl, call *ast.CallExpr) {
	var sig *types.Signature
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if fn, ok := fd.pkg.Info.Uses[fun].(*types.Func); ok {
			sig, _ = fn.Type().(*types.Signature)
		}
	case *ast.SelectorExpr:
		if fn, ok := fd.pkg.Info.Uses[fun.Sel].(*types.Func); ok {
			sig, _ = fn.Type().(*types.Signature)
		} else if sel := fd.pkg.Info.Selections[fun]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok {
				sig, _ = fn.Type().(*types.Signature)
			}
		}
	}
	if sig == nil {
		return
	}
	params := sig.Params()
	// Account for variadic: final []T params are passed as slices (pointers
	// to backing arrays), so individual elements are not copied by the call.
	offset := 0
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if s := fd.pkg.Info.Selections[sel]; s != nil && s.Kind() == types.MethodVal {
			// Receiver is implicit argument 0 in the selection signature;
			// call.Args starts after it, so no offset adjustment needed.
			_ = offset
		}
	}
	for i, arg := range call.Args {
		if i >= params.Len() {
			break
		}
		pt := params.At(i).Type()
		// Skip variadic slot: arguments flow into a slice, not copied.
		if params.At(i) == params.At(params.Len()-1) && sig.Variadic() && i == params.Len()-1 {
			if _, isSlice := underlying(pt).(*types.Slice); isSlice {
				continue
			}
		}
		if _, ok := arg.(*ast.UnaryExpr); ok {
			continue // &x passed is fine regardless
		}
		// Compare argument's actual type to the parameter: when the
		// parameter itself contains a lock, the arg is copied by value.
		if info, ok := containsLock(pt); ok {
			// &arg expressions already skipped; report only if the argument
			// is itself a value (not address-taken).
			if _, isComp := arg.(*ast.CompositeLit); isComp {
				// T{} passed by value copies a fresh lock — still flagged by
				// vet for user structs, but very common for one-shot use;
				// report as suspect only.
				info2 := info
				p.reportCopyConf(fd, arg.Pos(), info2, config.Suspect,
					"call passes "+info.name+" by value")
				continue
			}
			p.reportCopy(fd, arg.Pos(), info, "call passes "+info.name+" by value")
		}
	}
}

// inspectCopylockRange flags "for _, v := range" where v is a lock copy.
func (p *Program) inspectCopylockRange(fd *funcDecl, s *ast.RangeStmt) {
	if s.Value == nil {
		return
	}
	id, ok := s.Value.(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	if v, ok := fd.pkg.Info.Defs[id].(*types.Var); ok {
		if info, hit := containsLock(v.Type()); hit {
			p.reportCopy(fd, s.Value.Pos(), info, "range copies a value containing "+info.name+"; use indexes or pointers")
		}
	}
}

// inspectCopylockReturn flags returning a lock-containing value.
func (p *Program) inspectCopylockReturn(fd *funcDecl, s *ast.ReturnStmt) {
	for _, rx := range s.Results {
		t := fd.pkg.Info.TypeOf(rx)
		if t == nil {
			continue
		}
		if _, ok := rx.(*ast.UnaryExpr); ok {
			continue
		}
		if info, hit := containsLock(t); hit {
			p.reportCopy(fd, rx.Pos(), info, "return copies a value containing "+info.name)
		}
	}
}

// inspectCopylockSend flags channel sends of lock-containing values.
func (p *Program) inspectCopylockSend(fd *funcDecl, s *ast.SendStmt) {
	t := fd.pkg.Info.TypeOf(s.Value)
	if t == nil {
		return
	}
	if info, ok := containsLock(t); ok {
		p.reportCopy(fd, s.Value.Pos(), info, "channel send copies a value containing "+info.name)
	}
}

func (p *Program) reportCopy(fd *funcDecl, pos token.Pos, info lockInfo, msg string) {
	p.reportCopyConf(fd, pos, info, confidenceFor(info), msg)
}

// reportCopyConf emits a copied-lock diagnostic at pos with the given level.
func (p *Program) reportCopyConf(fd *funcDecl, pos token.Pos, info lockInfo, conf config.Confidence, msg string) {
	fd.diags = append(fd.diags, &Diagnostic{
		Category:   config.CopiedLock,
		Confidence: conf,
		Pos:        loader.PosOf(p.fset, pos),
		Message:    msg,
	})
}
