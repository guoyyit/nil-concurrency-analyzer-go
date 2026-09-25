package analysis

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// lockAction describes one lock-related call.
type lockAction struct {
	target string
	pos    token.Pos
	op     string
}

// checkUnlockPaths finds mutexes that are locked on a path but not unlocked on
// every path before the function returns or falls off the end.
func (p *Program) checkUnlockPaths() {
	if !p.cfg.IsEnabled(config.UnlockPath) {
		return
	}
	for _, fd := range p.funcs {
		if fd.decl.Body == nil {
			continue
		}
		u := &unlockWalker{
			pg:       p,
			fd:       fd,
			actions:  collectLockActions(fd),
			reported: map[string]bool{},
		}
		u.block(fd.decl.Body.List, map[string]lockFlowState{})
	}
}

type unlockWalker struct {
	pg       *Program
	fd       *funcDecl
	actions  map[token.Pos]lockAction
	reported map[string]bool
}

// block threads the held-lock state through statements. Lock/Unlock calls and
// branches merge the state; reports are emitted only at terminal points
// (returns and fall-off), so a lock released after an if-statement is never
// mistaken for a leak on the fall-through path.
func (u *unlockWalker) block(list []ast.Stmt, held map[string]lockFlowState) {
	for _, st := range list {
		cont, terminates := u.stmt(st, held)
		if terminates {
			return
		}
		held = cont
	}
	u.reportHeld(held) // falling off the end
}

// stmt returns the held state on the fall-through path and whether control
// cannot continue past this statement on every path (return).
func (u *unlockWalker) stmt(st ast.Stmt, held map[string]lockFlowState) (map[string]lockFlowState, bool) {
	switch s := st.(type) {
	case *ast.BlockStmt:
		u.block(s.List, cloneHeld(held))
		return held, false

	case *ast.ExprStmt:
		u.applyExpr(s.X, held)
		return held, false

	case *ast.AssignStmt:
		for _, rx := range s.Rhs {
			u.applyExpr(rx, held)
		}
		return held, false

	case *ast.GoStmt:
		u.applyExpr(s.Call, held)
		return held, false
	case *ast.DeferStmt:
		// defer mu.Unlock() runs on every function exit.
		if isUnlockCall(u.fd, s.Call) {
			if key, ok := lockTargetKey(u.fd, s.Call); ok {
				held[key] = lockUnlocked
			}
		}
		return held, false

	case *ast.IfStmt:
		if s.Init != nil {
			cont, _ := u.stmt(s.Init, held)
			held = cont
		}
		thenHeld := cloneHeld(held)
		u.block(s.Body.List, thenHeld) // terminal reports happen inside
		thenReturns := blockReturns(s.Body.List)

		var elseHeld map[string]lockFlowState
		elseReturns := false
		if s.Else != nil {
			elseHeld = cloneHeld(held)
			_, elseReturns = u.stmt(s.Else, elseHeld)
		} else {
			elseHeld = cloneHeld(held) // absent else: fall-through
		}
		if thenReturns && elseReturns {
			return held, true // both paths returned
		}
		if thenReturns {
			return elseHeld, false // only the else/fall-through continues
		}
		if elseReturns {
			return thenHeld, false
		}
		return mergeHeld(thenHeld, elseHeld), false

	case *ast.ForStmt:
		// Zero-iteration path continues with entry state; body may lock.
		bodyHeld := cloneHeld(held)
		u.block(s.Body.List, bodyHeld)
		return mergeHeld(held, bodyHeld), false

	case *ast.RangeStmt:
		bodyHeld := cloneHeld(held)
		u.block(s.Body.List, bodyHeld)
		return mergeHeld(held, bodyHeld), false

	case *ast.SwitchStmt:
		var exits []map[string]lockFlowState
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			h := cloneHeld(held)
			terminates := false
			u.block(clause.Body, h)
			if !blockReturns(clause.Body) {
				exits = append(exits, h)
			}
			_ = terminates
		}
		if len(exits) == 0 {
			return held, true // every case returns
		}
		out := held
		for _, h := range exits {
			out = mergeHeld(out, h)
		}
		return out, false

	case *ast.TypeSwitchStmt:
		var exits []map[string]lockFlowState
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			h := cloneHeld(held)
			u.block(clause.Body, h)
			if !blockReturns(clause.Body) {
				exits = append(exits, h)
			}
		}
		if len(exits) == 0 {
			return held, true
		}
		out := held
		for _, h := range exits {
			out = mergeHeld(out, h)
		}
		return out, false

	case *ast.SelectStmt:
		var exits []map[string]lockFlowState
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CommClause)
			h := cloneHeld(held)
			if clause.Comm != nil {
				u.stmt(asStmt(clause.Comm), h)
			}
			u.block(clause.Body, h)
			if !blockReturns(clause.Body) {
				exits = append(exits, h)
			}
		}
		// A select without a default may block forever; still report
		// conservative merge and never assume terminates.
		if len(exits) == 0 {
			return held, false
		}
		out := held
		for _, h := range exits {
			out = mergeHeld(out, h)
		}
		return out, false

	case *ast.LabeledStmt:
		return u.stmt(s.Stmt, held)

	case *ast.ReturnStmt:
		u.reportHeld(held)
		return held, true

	default:
		if st != nil {
			ast.Inspect(st, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					u.applyCall(call, held)
				}
				return true
			})
		}
		return held, false
	}
}

// blockReturns conservatively reports whether the statement list ends every
// path with a return (used to decide if a branch can fall through).
func blockReturns(list []ast.Stmt) bool {
	for _, st := range list {
		switch s := st.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.LabeledStmt:
			if blockReturns([]ast.Stmt{s.Stmt}) {
				return true
			}
		case *ast.BlockStmt:
			if blockReturns(s.List) {
				return true
			}
		case *ast.IfStmt:
			if s.Else != nil && blockReturns(s.Body.List) && branchReturns(s.Else) {
				return true
			}
		}
	}
	return false
}

func branchReturns(st ast.Stmt) bool {
	if b, ok := st.(*ast.BlockStmt); ok {
		return blockReturns(b.List)
	}
	return blockReturns([]ast.Stmt{st})
}

func (u *unlockWalker) applyExpr(x ast.Expr, held map[string]lockFlowState) {
	if x == nil {
		return
	}
	ast.Inspect(x, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			u.applyCall(call, held)
		}
		return true
	})
}

func (u *unlockWalker) applyCall(call *ast.CallExpr, held map[string]lockFlowState) {
	if a, ok := u.actions[call.Pos()]; ok {
		switch a.op {
		case "Lock", "RLock":
			held[a.target] = lockLocked
		case "Unlock", "RUnlock":
			held[a.target] = lockUnlocked
		}
	}
}

// reportHeld emits one diagnostic per still-held lock at a terminal point.
func (u *unlockWalker) reportHeld(held map[string]lockFlowState) {
	var keys []string
	for k, v := range held {
		if v == lockLocked || v == lockMaybe {
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		if u.reported[key] {
			continue
		}
		var pos token.Pos
		for p, a := range u.actions {
			if a.target == key && (a.op == "Lock" || a.op == "RLock") {
				pos = p
			}
		}
		if !pos.IsValid() {
			continue
		}
		u.reported[key] = true
		u.pg.reportUnlock(u.fd, pos)
	}
}

func cloneHeld(m map[string]lockFlowState) map[string]lockFlowState {
	cp := make(map[string]lockFlowState, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func mergeHeld(a, b map[string]lockFlowState) map[string]lockFlowState {
	out := cloneHeld(a)
	for k, v := range b {
		out[k] = mergeLock(out[k], v)
	}
	return out
}

func asStmt(n ast.Stmt) ast.Stmt { return n }

// collectLockActions gathers Lock/Unlock calls with static targets.
func collectLockActions(fd *funcDecl) map[token.Pos]lockAction {
	out := map[token.Pos]lockAction{}
	if fd.decl.Body == nil {
		return out
	}
	ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		s := fd.pkg.Info.Selections[sel]
		name, ok := lockMethodName(s)
		if !ok {
			return true
		}
		if name != "Lock" && name != "RLock" && name != "Unlock" && name != "RUnlock" {
			return true
		}
		if key, ok := lockTargetKey(fd, call); ok {
			out[call.Pos()] = lockAction{target: key, pos: call.Pos(), op: name}
		}
		return true
	})
	return out
}

func lockTargetKey(fd *funcDecl, call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	return exprKey(fd, sel.X)
}

func exprKey(fd *funcDecl, x ast.Expr) (string, bool) {
	switch t := x.(type) {
	case *ast.Ident:
		if v, ok := fd.pkg.Info.Uses[t].(*types.Var); ok {
			return "var:" + v.Id(), true
		}
		if v, ok := fd.pkg.Info.Defs[t].(*types.Var); ok {
			return "var:" + v.Id(), true
		}
	case *ast.SelectorExpr:
		if obj, ok := fd.pkg.Info.Uses[t.Sel].(*types.Var); ok {
			return "var:" + obj.Id(), true
		}
		if sel := fd.pkg.Info.Selections[t]; sel != nil {
			if obj, ok := sel.Obj().(*types.Var); ok {
				if base, ok := exprKey(fd, t.X); ok {
					return base + ".field:" + obj.Id(), true
				}
			}
		}
	}
	return "", false
}

func isUnlockCall(fd *funcDecl, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	s := fd.pkg.Info.Selections[sel]
	name, ok := lockMethodName(s)
	return ok && (name == "Unlock" || name == "RUnlock")
}

func (p *Program) reportUnlock(fd *funcDecl, pos token.Pos) {
	fd.diags = append(fd.diags, &Diagnostic{
		Category:   config.UnlockPath,
		Confidence: config.Suspect,
		Pos:        loader.PosOf(p.fset, pos),
		Message:    "mutex is not unlocked on every reachable path from this Lock (use defer mu.Unlock())",
	})
}
