package concurrency

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// lockMethods 列出标准库中“复制后会导致锁语义错误”的类型。
// 判定方式与 go vet copylocks 一致：这些类型本身含 Lock 方法（指针接收者），
// 或被 //go:nolock 之外的内部 noCopy 标记。
var lockTypeNames = map[string]bool{
	"sync.Mutex":     true,
	"sync.RWMutex":   true,
	"sync.WaitGroup": true,
	"sync.Cond":      true,
	"sync.Once":      true,
	"sync.Map":       false, // sync.Map 含 Mutex，但 vet 对其直接复制也报；置 true 更严格
	"atomic.Bool":    true,
	"atomic.Int32":   true,
	"atomic.Int64":   true,
	"atomic.Uint32":  true,
	"atomic.Uint64":  true,
	"atomic.Uintptr": true,
	"atomic.Float64": true,
	"atomic.Pointer": true,
	"errgroup.Group": true,
}

// ValProvider 由 nilflow 提供：查询 send 语句通道表达式在程序点的空值状态。
type ValProvider interface {
	// SendChanKind 返回某发送语句通道的空值状态（0 未知/1 nil/2 非nil/3 可能nil）。
	SendChanKind(pos token.Pos) (int, bool)
}

// copylockChecker 检查互斥/同步原语按值复制。
type copylockChecker struct {
	info *types.Info
	fset *token.FileSet
	out  []diag.Diagnostic
}

func checkCopylocks(fset *token.FileSet, info *types.Info, files []*ast.File) []diag.Diagnostic {
	c := &copylockChecker{info: info, fset: fset}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.RangeStmt:
				c.checkCopy(x.Pos(), x.X, "range copies value containing lock")
			case *ast.SendStmt:
				c.checkCopy(x.Pos(), x.Value, "send copies value containing lock")
			case *ast.CallExpr:
				c.checkCallArgs(x)
			case *ast.AssignStmt:
				c.checkAssign(x)
			case *ast.CompositeLit:
				c.checkComposite(x)
			case *ast.DeclStmt:
				c.checkDecl(x)
			case *ast.FuncDecl:
				c.checkFuncSignature(x)
			}
			return true
		})
	}
	return c.out
}

func (c *copylockChecker) emit(pos token.Pos, msg string, conf diag.Confidence) {
	f, ln, col := fileLineCol(c.fset, pos)
	c.out = append(c.out, diag.Diagnostic{
		Category:   diag.CatMutexCopy,
		Position:   diag.Position{File: f, Line: ln, Column: col},
		Message:    msg,
		Confidence: conf,
	})
}

func (c *copylockChecker) checkCopy(pos token.Pos, e ast.Expr, msg string) {
	if e == nil || c.info == nil {
		return
	}
	tv, ok := c.info.Types[e]
	if !ok {
		return
	}
	if containsLockType(tv.Type) {
		c.emit(pos, msg+": "+typeString(tv.Type), diag.ConfidenceHigh)
	}
}

func (c *copylockChecker) checkCallArgs(call *ast.CallExpr) {
	for _, arg := range call.Args {
		// 解引用实参 *p 隐含一次按值复制。
		if star, ok := arg.(*ast.StarExpr); ok {
			c.checkCopy(star.Pos(), star.X, "call copies dereferenced value containing lock")
			continue
		}
		// 非变参按值传参复制。
		c.checkCopy(arg.Pos(), arg, "call argument copies value containing lock")
	}
}

func (c *copylockChecker) checkAssign(a *ast.AssignStmt) {
	for _, r := range a.Rhs {
		if star, ok := r.(*ast.StarExpr); ok {
			c.checkCopy(star.Pos(), star.X, "assignment copies dereferenced value containing lock")
			continue
		}
		c.checkCopy(r.Pos(), r, "assignment copies value containing lock")
	}
}

func (c *copylockChecker) checkComposite(cl *ast.CompositeLit) {
	// T{mu} 当 T 本身就是锁类型的数组/切片/结构体值时属复制；
	// 仅在元素直接为锁值时报告，避免嵌套场景噪音。
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			el = kv.Value
		}
		c.checkCopy(el.Pos(), el, "composite literal copies value containing lock")
	}
}

func (c *copylockChecker) checkDecl(d *ast.DeclStmt) {
	gd, ok := d.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, sp := range gd.Specs {
		vs, ok := sp.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, val := range vs.Values {
			c.checkCopy(val.Pos(), val, "value declaration copies value containing lock")
		}
	}
}

func (c *copylockChecker) checkFuncSignature(fd *ast.FuncDecl) {
	if fd.Type == nil {
		return
	}
	if fd.Recv != nil {
		// 值接收者：调用时复制接收者。
		for _, field := range fd.Recv.List {
			if field.Type == nil {
				continue
			}
			t := c.info.TypeOf(field.Type)
			if t != nil && containsLockType(t) {
				c.emit(fd.Type.Pos(), "method has value receiver that copies value containing lock: "+typeString(t),
					diag.ConfidenceHigh)
			}
		}
	}
	// 按值传参的复制在每个调用点以 high 报告（见 checkCallArgs），
	// 不对参数签名重复报告。
	// 返回锁值的函数在返回时复制：在结果签名处统一报一次可疑，
	// 避免每个 return 语句重复报告同一缺陷。
	if fd.Type.Results == nil {
		return
	}
	for _, field := range fd.Type.Results.List {
		if field.Type == nil {
			continue
		}
		t := c.info.TypeOf(field.Type)
		if t != nil && !isSlice(t) && containsLockType(t) {
			c.emit(field.Pos(), "function returns value containing lock by value: "+typeString(t),
				diag.ConfidenceSuspicious)
		}
	}
}

// containsLockType 判断类型（含结构体字段、数组元素）是否为或嵌入了锁类型。
func containsLockType(t types.Type) bool {
	if t == nil {
		return false
	}
	if isKnownLockNamed(t) {
		return true
	}
	return lockDepth(t, map[types.Type]bool{}) > 0
}

func lockDepth(t types.Type, seen map[types.Type]bool) int {
	if t == nil {
		return 0
	}
	t = t.Underlying()
	if seen[t] {
		return 0
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if isKnownLockNamed(f.Type()) || lockDepth(f.Type(), seen) > 0 {
				return 1
			}
		}
	case *types.Array:
		if isKnownLockNamed(u.Elem()) || lockDepth(u.Elem(), seen) > 0 {
			return 1
		}
	case *types.Pointer:
		return 0 // 指针不复制底层锁
	case *types.Slice, *types.Map, *types.Chan, *types.Interface:
		return 0
	}
	if isKnownLockNamed(t) {
		return 1
	}
	return 0
}

func isKnownLockNamed(t types.Type) bool {
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	name := obj.Pkg().Path() + "." + obj.Name()
	return lockTypeNames[name]
}

func isSlice(t types.Type) bool {
	_, ok := t.Underlying().(*types.Slice)
	return ok
}

func typeString(t types.Type) string { return t.String() }

func fileLineCol(fset *token.FileSet, pos token.Pos) (string, int, int) {
	p := fset.Position(pos)
	return p.Filename, p.Line, p.Column
}
