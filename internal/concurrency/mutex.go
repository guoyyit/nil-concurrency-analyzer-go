package concurrency

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// lockStateKind 描述某个互斥量在一条路径上的状态。
type lockStateKind int

const (
	lockUnlocked lockStateKind = iota
	lockLocked
	lockDeferredUnlock // 已注册 defer Unlock，退出时必然解锁
	lockMaybe          // 分支合并后状态不一致
)

// lockFact 记录一处加锁点及其当前状态。
type lockFact struct {
	obj      interface{} // 锁的语义对象（*types.Var 或选择器路径）
	pos      token.Pos   // Lock() 调用位置
	state    lockStateKind
	exprText string
}

// mutexChecker 在单个函数内做路径敏感的 Lock/Unlock 配对检查。
type mutexChecker struct {
	info *types.Info
	fset *token.FileSet
	out  []diag.Diagnostic
}

func checkMutexPaths(fd *ast.FuncDecl, fset *token.FileSet, info *types.Info) []diag.Diagnostic {
	if fd.Body == nil {
		return nil
	}
	mc := &mutexChecker{info: info, fset: fset}
	st := newLockEnv(nil)
	mc.walk(fd.Body, st)
	mc.reportExitLeaks(fd, st)
	return mc.out
}

type lockEnv struct {
	parent *lockEnv
	// 按接收者表达式对象索引的锁。
	byKey map[interface{}]*lockFact
}

func newLockEnv(parent *lockEnv) *lockEnv {
	return &lockEnv{parent: parent, byKey: map[interface{}]*lockFact{}}
}

func (e *lockEnv) clone() *lockEnv {
	c := newLockEnv(e.parent)
	for k, v := range e.byKey {
		f := *v
		c.byKey[k] = &f
	}
	return c
}

// join 合并分支环境：同一把锁状态一致则保留，否则记 Maybe。
func (e *lockEnv) join(o *lockEnv) *lockEnv {
	m := newLockEnv(nil)
	keys := map[interface{}]bool{}
	for k := range e.all() {
		keys[k] = true
	}
	for k := range o.all() {
		keys[k] = true
	}
	for k := range keys {
		a := e.lookup(k)
		b := o.lookup(k)
		switch {
		case a != nil && b != nil:
			f := *a
			if a.state != b.state {
				// 一边已 defer 解锁，一边仍锁定：整体安全退出，视作已安排解锁。
				if a.state == lockDeferredUnlock || b.state == lockDeferredUnlock {
					f.state = lockDeferredUnlock
				} else {
					f.state = lockMaybe
				}
			}
			m.byKey[k] = &f
		case a != nil:
			f := *a
			// 某分支提前 return 已由可达性处理；此处若 b 不存在表示另一分支未加锁，安全。
			m.byKey[k] = &f
		case b != nil:
			f := *b
			m.byKey[k] = &f
		}
	}
	return m
}

func (e *lockEnv) all() map[interface{}]*lockFact {
	res := map[interface{}]*lockFact{}
	for cur := e; cur != nil; cur = cur.parent {
		for k, v := range cur.byKey {
			res[k] = v
		}
	}
	return res
}

func (e *lockEnv) lookup(k interface{}) *lockFact {
	for cur := e; cur != nil; cur = cur.parent {
		if f, ok := cur.byKey[k]; ok {
			return f
		}
	}
	return nil
}

func (mc *mutexChecker) emit(pos token.Pos, cat diag.Category, conf diag.Confidence, msg string) {
	f, ln, col := fileLineCol(mc.fset, pos)
	mc.out = append(mc.out, diag.Diagnostic{
		Category: cat, Position: diag.Position{File: f, Line: ln, Column: col},
		Message: msg, Confidence: conf,
	})
}

// lockKey 返回 Lock()/Unlock() 接收者的稳定标识。
func lockKey(info *types.Info, x ast.Expr) interface{} {
	switch r := x.(type) {
	case *ast.Ident:
		if obj := info.ObjectOf(r); obj != nil {
			return obj
		}
		return r
	case *ast.SelectorExpr:
		// 以选择器表达式整体的对象链做 key；用 X 对象 + 字段名。
		if sel, ok := r.X.(*ast.Ident); ok {
			if obj := info.ObjectOf(sel); obj != nil {
				return struct {
					base interface{}
					sel  string
				}{base: obj, sel: r.Sel.Name}
			}
		}
		return r
	}
	return x
}

// walk 返回语句执行后的锁环境；return 等终止语句返回 nil。
func (mc *mutexChecker) walk(stmt ast.Stmt, env *lockEnv) *lockEnv {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		cur := env
		for _, x := range s.List {
			if cur == nil {
				return nil
			}
			cur = mc.walk(x, cur)
		}
		return cur
	case *ast.ExprStmt:
		return mc.transferCall(s.X, env)
	case *ast.AssignStmt:
		for _, r := range s.Rhs {
			if call, ok := r.(*ast.CallExpr); ok {
				env = mc.transferCall(call, env)
			}
		}
		return env
	case *ast.IfStmt:
		if s.Init != nil {
			env = mc.walk(s.Init, env)
		}
		then := mc.walkBlock(s.Body, env.clone())
		els := env.clone()
		if s.Else != nil {
			els = mc.walk(s.Else, els)
		}
		return joinEnvs(then, els)
	case *ast.ForStmt:
		if s.Init != nil {
			env = mc.walk(s.Init, env)
		}
		body := mc.walkBlock(s.Body, env.clone())
		out := env.clone()
		return joinEnvs(body, out)
	case *ast.RangeStmt:
		body := mc.walkBlock(s.Body, env.clone())
		return joinEnvs(body, env.clone())
	case *ast.SwitchStmt:
		if s.Init != nil {
			env = mc.walk(s.Init, env)
		}
		var ends []*lockEnv
		var def *lockEnv
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			end := mc.walkStmts(cc.Body, env.clone())
			if cc.List == nil {
				def = end
			} else {
				ends = append(ends, end)
			}
		}
		m := joinEnvs(joinEnvList(ends), def)
		if m == nil {
			m = env
		}
		return m
	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			env = mc.walk(s.Init, env)
		}
		if a, ok := s.Assign.(*ast.AssignStmt); ok {
			env = mc.walk(a, env)
		}
		var ends []*lockEnv
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			ends = append(ends, mc.walkStmts(cc.Body, env.clone()))
		}
		if m := joinEnvList(ends); m != nil {
			return m
		}
		return env
	case *ast.SelectStmt:
		var ends []*lockEnv
		for _, c := range s.Body.List {
			cc := c.(*ast.CommClause)
			b := env.clone()
			if cc.Comm != nil {
				b = mc.walk(cc.Comm, b)
			}
			if b != nil {
				ends = append(ends, mc.walkStmts(cc.Body, b))
			}
		}
		if m := joinEnvList(ends); m != nil {
			return m
		}
		return env
	case *ast.ReturnStmt:
		mc.reportExitReturn(s, env)
		return nil
	case *ast.GoStmt, *ast.DeferStmt:
		// defer mu.Unlock() 在语句出现时即登记“退出时解锁”。
		if d, ok := stmt.(*ast.DeferStmt); ok {
			return mc.transferDeferUnlock(d.Call, env)
		}
		return mc.transferCall(stmt.(*ast.GoStmt).Call, env)
	case *ast.LabeledStmt:
		return mc.walk(s.Stmt, env)
	case *ast.BranchStmt, *ast.EmptyStmt:
		return env
	}
	return env
}

func (mc *mutexChecker) walkBlock(b *ast.BlockStmt, env *lockEnv) *lockEnv {
	return mc.walk(b, env)
}

func (mc *mutexChecker) walkStmts(list []ast.Stmt, env *lockEnv) *lockEnv {
	cur := env
	for _, s := range list {
		if cur == nil {
			return nil
		}
		cur = mc.walk(s, cur)
	}
	return cur
}

// transferCall 处理普通语句位置的 Lock/Unlock。
func (mc *mutexChecker) transferCall(e ast.Expr, env *lockEnv) *lockEnv {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return env
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return env
	}
	key := lockKey(mc.info, sel.X)
	switch sel.Sel.Name {
	case "Lock", "RLock":
		if f := env.lookup(key); f != nil && f.state == lockLocked {
			mc.emit(call.Pos(), diag.CatMutexNotUnlocked, diag.ConfidenceHigh,
				"mutex is already locked; re-locking deadlocks")
		}
		env.byKey[key] = &lockFact{obj: key, pos: call.Pos(), state: lockLocked}
	case "Unlock", "RUnlock":
		if f := env.lookup(key); f != nil {
			if f.state == lockDeferredUnlock {
				mc.emit(call.Pos(), diag.CatMutexNotUnlocked, diag.ConfidenceSuspicious,
					"mutex already scheduled to unlock via defer; double unlock possible")
			}
			delete(env.byKey, key)
			for cur := env.parent; cur != nil; cur = cur.parent {
				delete(cur.byKey, key)
			}
		} else {
			mc.emit(call.Pos(), diag.CatMutexNotUnlocked, diag.ConfidenceSuspicious,
				"unlock of unlocked mutex")
		}
	case "TryLock", "TryRLock":
		env.byKey[key] = &lockFact{obj: key, pos: call.Pos(), state: lockMaybe}
	}
	return env
}

func (mc *mutexChecker) transferDeferUnlock(call *ast.CallExpr, env *lockEnv) *lockEnv {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return env
	}
	if sel.Sel.Name == "Unlock" || sel.Sel.Name == "RUnlock" {
		key := lockKey(mc.info, sel.X)
		if f := env.lookup(key); f != nil {
			f.state = lockDeferredUnlock
		} else {
			// defer Unlock 出现在 Lock 之前：仍登记，避免对随后 Lock 的退出误报。
			env.byKey[key] = &lockFact{obj: key, pos: call.Pos(), state: lockDeferredUnlock}
		}
	}
	return env
}

// reportExitReturn 检查 return 时仍持有（且未安排 defer 解锁）的锁。
func (mc *mutexChecker) reportExitReturn(r *ast.ReturnStmt, env *lockEnv) {
	for _, f := range env.all() {
		if f.state == lockLocked || f.state == lockMaybe {
			mc.emit(f.pos, diag.CatMutexNotUnlocked, diag.ConfidenceHigh,
				"mutex locked on return path without matching unlock")
		}
	}
}

// reportExitLeaks 检查函数自然结束时仍持有的锁。
func (mc *mutexChecker) reportExitLeaks(fd *ast.FuncDecl, env *lockEnv) {
	if env == nil {
		return
	}
	for _, f := range env.all() {
		if f.state == lockLocked || f.state == lockMaybe {
			mc.emit(f.pos, diag.CatMutexNotUnlocked, diag.ConfidenceHigh,
				"mutex may remain locked when function returns")
		}
	}
}

func joinEnvs(a, b *lockEnv) *lockEnv {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return a.join(b)
}

func joinEnvList(ss []*lockEnv) *lockEnv {
	var m *lockEnv
	for _, s := range ss {
		m = joinEnvs(m, s)
	}
	return m
}
