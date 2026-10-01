package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// eval 在给定状态下求值表达式：触发其中的解引用检查，并返回表达式的空值抽象。
func (a *intraAnalyzer) eval(e ast.Expr, st *flowState) Val {
	if e == nil {
		return vUnknown()
	}
	switch n := e.(type) {
	case *ast.Ident:
		if n.Name == "nil" {
			return vNil()
		}
		if obj := a.info.ObjectOf(n); obj != nil {
			if v, ok := st.vars[objectKey(obj)]; ok {
				return v
			}
		}
		return vUnknown()

	case *ast.BasicLit:
		return vNonNil()

	case *ast.UnaryExpr:
		if n.Op == token.AND { // &x
			a.eval(n.X, st)
			return vNonNil()
		}
		if n.Op == token.MUL { // *p 解引用
			v := a.eval(n.X, st)
			a.checkPtrDeref(n, n.X, v, st)
			return vUnknown()
		}
		a.eval(n.X, st)
		return vUnknown()

	case *ast.ParenExpr:
		return a.eval(n.X, st)

	case *ast.StarExpr: // 类型表达式中出现时不检查
		if a.isTypeExpr(n) {
			return vUnknown()
		}
		v := a.eval(n.X, st)
		a.checkPtrDeref(n, n.X, v, st)
		return vUnknown()

	case *ast.SelectorExpr:
		// 字段访问：当接收者是指针时，字段选择隐含一次指针解引用。
		v := a.eval(n.X, st)
		sel := a.info.Selections[n]
		if sel != nil && sel.Kind() == types.FieldVal {
			if isPointerType(exprType(n.X, a.info)) {
				a.checkPtrDeref(n, n.X, v, st)
			}
		}
		if obj := a.info.Uses[n.Sel]; obj != nil {
			if v, ok := st.vars[objectKey(obj)]; ok {
				return v
			}
		}
		return vUnknown()

	case *ast.IndexExpr:
		idx := a.eval(n.Index, st)
		_ = idx
		cv := a.eval(n.X, st)
		// 对 *[N]T 数组的索引属于指针解引用；map/slice 索引对 nil 不崩溃（map 写入除外，保守不报）。
		if isPointerToArray(n.X, a.info) {
			a.checkPtrDeref(n, n.X, cv, st)
		}
		return vUnknown()

	case *ast.BinaryExpr:
		lv := a.eval(n.X, st)
		rv := a.eval(n.Y, st)
		_ = lv
		_ = rv
		// 比较结果本身非 nil。
		return vNonNil()

	case *ast.CallExpr:
		return a.evalCall(n, st)

	case *ast.TypeAssertExpr:
		v := a.eval(n.X, st)
		// 带 ok 形式不会 panic；不带 ok 时类型错误会 panic，但与空值无关。
		_ = v
		return vUnknown()

	case *ast.CompositeLit:
		for _, el := range n.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				a.eval(kv.Value, st)
			} else {
				a.eval(el, st)
			}
		}
		return vNonNil()

	case *ast.FuncLit:
		// 闭包体不在当前路径内执行，保守跳过（已知局限，见文档）。
		return vNonNil()

	case *ast.SliceExpr:
		a.eval(n.X, st)
		a.eval(n.Low, st)
		a.eval(n.High, st)
		a.eval(n.Max, st)
		return vUnknown()

	case *ast.KeyValueExpr:
		a.eval(n.Key, st)
		return a.eval(n.Value, st)

	case *ast.FuncType, *ast.ArrayType, *ast.StructType, *ast.MapType,
		*ast.ChanType, *ast.InterfaceType, *ast.Ellipsis:
		return vUnknown()
	}
	return vUnknown()
}

// evalMulti 求值多值上下文（x, ok := m[k] / v.(T) / <-ch / f()）。
func (a *intraAnalyzer) evalMulti(e ast.Expr, st *flowState) ([]Val, bool) {
	switch n := e.(type) {
	case *ast.IndexExpr: // map 索引（也可能是 *array，后者单值）
		a.eval(n.X, st)
		a.eval(n.Index, st)
		// 两个结果：value(未知), ok(未知非nil概念)。
		return []Val{vUnknown(), vUnknown()}, true
	case *ast.TypeAssertExpr:
		a.eval(n.X, st)
		return []Val{vUnknown(), vUnknown()}, true
	case *ast.UnaryExpr:
		if n.Op == token.ARROW { // <-ch
			a.eval(n.X, st)
			return []Val{vUnknown(), vUnknown()}, true
		}
	case *ast.CallExpr:
		vals, ok := a.evalCallMulti(n, st)
		if ok {
			return vals, true
		}
	}
	return nil, false
}

// evalCall 处理单值调用，并传播被调函数摘要。
func (a *intraAnalyzer) evalCall(call *ast.CallExpr, st *flowState) Val {
	vals, ok := a.evalCallMulti(call, st)
	if ok && len(vals) >= 1 {
		return vals[0]
	}
	return vUnknown()
}

// evalCallMulti 求值调用，同时：
//  1. 检查入参解引用（被调函数摘要声明的解引用形参）；
//  2. 根据返回值摘要给各结果打上空值标记与调用出处。
func (a *intraAnalyzer) evalCallMulti(call *ast.CallExpr, st *flowState) ([]Val, bool) {
	// 先求值接收者与所有实参（会触发调用点处的解引用检查）。
	argVals := make([]Val, len(call.Args))
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		_ = a.eval(sel.X, st)
	}
	for i, arg := range call.Args {
		argVals[i] = a.eval(arg, st)
	}

	// 内置函数。
	if id, ok := call.Fun.(*ast.Ident); ok {
		switch id.Name {
		case "new":
			return []Val{vNonNil()}, true
		case "make":
			return []Val{vNonNil()}, true
		}
	}

	fn := calledFunc(call, a.info)
	if fn == nil || a.resolve == nil {
		return nil, false
	}
	sum := a.resolve(fn)

	// 入参解引用：实参确定为 nil 时，先尝试同包内联（内部缺陷带路径报告）；
	// 内联不可行（外部函数/无函数体）时，再用被调摘要的 DerefParams 回退报告。
	// 轻量发送分析模式不需要跨函数缺陷。
	var inlined bool
	if !a.env.SendsOnly {
		inlined = a.inlineNilArgs(call, fn, argVals, st)
	}
	if !inlined && sum != nil {
		for _, pi := range sum.DerefParams {
			av := argValueForParam(call, argVals, pi)
			if av.isNil() {
				a.emitDeref(call, fn, pi, av, sum.DerefPos[pi], st)
			}
		}
	}

	results := resultTypes(fn, a.info)
	if results == 0 {
		return []Val{}, true
	}
	out := make([]Val, results)
	for i := range out {
		out[i] = vUnknown()
		if sum != nil && i < len(sum.Results) {
			v := sum.Results[i]
			// 标记调用出处及相对 error/ok 兄弟返回值的条件空值，供条件精化。
			v.Call = &CallRef{Expr: call, Idx: i, Cond: condFor(sum.SiblingCond, i)}
			out[i] = v
		}
	}
	return out, true
}

// condFor 返回结果 resultIdx 关联的全部兄弟条件（同一数据可能对应多个兄弟）。
// CallRef 只携带单个指针，因此这里只保留最常用的 error/首个 bool 关联。
func condFor(conds []SiblingCond, resultIdx int) *SiblingCond {
	for i := range conds {
		if conds[i].ResultIdx == resultIdx {
			c := conds[i]
			return &c
		}
	}
	return nil
}

// checkPtrDeref 根据解引用点的空值状态产出诊断或证据。
func (a *intraAnalyzer) checkPtrDeref(node ast.Node, target ast.Expr, v Val, st *flowState) {
	// 轻量发送分析模式：只关心通道空值，不产出解引用诊断。
	if a.env.SendsOnly {
		return
	}
	// 内联场景：调用方已对该形参判空保护，则不报也不登记无保护解引用。
	if _, guarded := a.paramGuardKind(target); guarded {
		return
	}
	// 目标回溯到形参：登记供调用方跨函数传播（未知/可能 nil 均登记）。
	a.markParamDeref(target, v, node.Pos())
	var path []diag.CallNode
	if a.env.Caller != nil {
		path = buildCallPath(a.fset, a.env.Func, a.env.Caller, node.Pos())
	}
	pos := node.Pos()
	switch {
	case v.isNil():
		a.emit(pos, diag.CatNilDeref, diag.ConfidenceHigh, diag.CertaintyDefinite,
			"nil pointer dereference: expression is provably nil", path)
	case v.Kind == ValMaybeNil:
		a.emit(pos, diag.CatNilDeref, diag.ConfidenceSuspicious, diag.CertaintyPossible,
			"possible nil pointer dereference: value may be nil on some path", path)
	case v.Kind == ValNonNil && a.emitExc:
		a.emit(pos, diag.CatNilDeref, diag.ConfidenceSuspicious, diag.CertaintyExcluded,
			"dereference guarded: expression is provably non-nil on this branch", path)
	}
}

func (a *intraAnalyzer) emitDeref(call *ast.CallExpr, fn *types.Func, paramIdx int, v Val, derefPos token.Pos, st *flowState) {
	// 被调函数对该形参存在无条件解引用，实参确定 nil。
	var path []diag.CallNode
	if a.env.Caller != nil || a.env.Func != fn {
		path = buildCallPath(a.fset, fn, a.env.Caller, derefPos)
	}
	a.emit(call.Pos(), diag.CatNilDeref, diag.ConfidenceHigh, diag.CertaintyDefinite,
		"nil passed to "+fn.FullName()+" which dereferences parameter "+paramName(fn, paramIdx)+" without a nil guard",
		path)
}

func (a *intraAnalyzer) emit(pos token.Pos, cat diag.Category, conf diag.Confidence, cert diag.Certainty, msg string, path []diag.CallNode) {
	file, line, col := posAt(a.fset, pos)
	d := diag.Diagnostic{
		Category:   cat,
		Position:   diag.Position{File: file, Line: line, Column: col},
		Message:    msg,
		Confidence: conf,
		Certainty:  cert,
		CallPath:   path,
	}
	a.diags = append(a.diags, d)
	a.ldiags = append(a.ldiags, a.toLocDiag(d))
}

// toLocDiag 把刚产生的诊断连同函数锚点一起转为 FileSet 无关形式。
func (a *intraAnalyzer) toLocDiag(d diag.Diagnostic) locDiag {
	mainStart := fnStart{}
	if a.env.Decl != nil {
		p := a.fset.Position(a.env.Decl.Name.Pos())
		mainStart = fnStart{file: p.Filename, line: p.Line}
	}
	// 调用路径节点：沿 Caller 链可以拿到每个节点函数的声明行；
	// 这里用当前可见的锚点信息构造（Caller.CallPath 已含上层）。
	var starts []fnStart
	for _, n := range d.CallPath {
		st := fnStart{file: n.Position.File}
		if a.env.Caller != nil {
			// 尽量由调用路径链中的声明位置推导；找不到时保持 file 仅做同名锚定。
			if fs, ok := a.pathStarts[n.Func]; ok {
				st = fs
			}
		}
		starts = append(starts, st)
	}
	return toLocDiag(d, a.env.Func.FullName(), mainStart, starts)
}

func (a *intraAnalyzer) isTypeExpr(n *ast.StarExpr) bool {
	tv := a.info.Types[n]
	return tv.IsType()
}

func exprType(e ast.Expr, info *types.Info) types.Type {
	if tv, ok := info.Types[e]; ok {
		return tv.Type
	}
	return nil
}

func isPointerType(t types.Type) bool {
	_, ok := t.(*types.Pointer)
	return ok
}

func isPointerToArray(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]
	if !ok {
		return false
	}
	if p, ok := tv.Type.(*types.Pointer); ok {
		if _, ok := p.Elem().(*types.Array); ok {
			return true
		}
	}
	return false
}

// calledFunc 解析调用表达式指向的 *types.Func（含方法选择）。
func calledFunc(call *ast.CallExpr, info *types.Info) *types.Func {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if obj, ok := info.Uses[f]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	case *ast.SelectorExpr:
		if obj, ok := info.Uses[f.Sel]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	case *ast.IndexExpr:
		// 泛型实例化 f[T](...)
		if id, ok := f.X.(*ast.Ident); ok {
			if obj, ok := info.Uses[id]; ok {
				if fn, ok := obj.(*types.Func); ok {
					return fn
				}
			}
		}
	}
	return nil
}

func resultTypes(fn *types.Func, info *types.Info) int {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return 0
	}
	return sig.Results().Len()
}

// argValueForParam 把实参下标映射到形参下标（接收者占方法形参表第 0 位）。
func argValueForParam(call *ast.CallExpr, args []Val, paramIdx int) Val {
	offset := 0
	if _, ok := call.Fun.(*ast.SelectorExpr); ok {
		offset = 1
	}
	i := paramIdx - offset
	if i >= 0 && i < len(args) {
		return args[i]
	}
	return vUnknown()
}

func paramName(fn *types.Func, idx int) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return "?"
	}
	if sig.Recv() != nil {
		if idx == 0 {
			return "receiver"
		}
		idx--
	}
	if idx >= 0 && idx < sig.Params().Len() {
		v := sig.Params().At(idx)
		if v.Name() != "" {
			return v.Name()
		}
	}
	return "#" + itoa(idx+1)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
