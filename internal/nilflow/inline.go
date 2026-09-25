package nilflow

import (
	"go/ast"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// inlineNilArgs 在调用点对确定为 nil 的实参做一次上下文敏感的内联分析：
// 用实参空值特化被调函数的形参，运行一遍被调函数体，使函数内部由该 nil
// 引发的解引用（含再向下一层传播）能够在调用点路径上被发现。
// 返回 true 表示被调函数已被内联（同包函数内部缺陷由内联统一报告，
// 调用点的摘要回退不再重复报告）。
func (a *intraAnalyzer) inlineNilArgs(call *ast.CallExpr, fn *types.Func, argVals []Val, st *flowState) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	decl := a.findDecl(fn)
	if decl == nil || decl.Body == nil {
		return false
	}
	if a.activeCalls[fn] {
		return false // 递归守卫
	}
	// 确定 nil 或可能 nil 的实参/接收者都值得内联：
	// 前者在内部产生 definite，后者产生 possible；确定非 nil 与未知不内联。
	interesting := func(v Val) bool { return v.Kind == ValNil || v.Kind == ValMaybeNil }
	hasNil := false
	for _, v := range argVals {
		if interesting(v) {
			hasNil = true
		}
	}
	if sig.Recv() != nil {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if interesting(a.eval(sel.X, st)) {
				hasNil = true
			}
		}
	}
	if !hasNil {
		return false
	}

	sub := a.subAnalyzer(fn, decl, call)
	sub.activeCalls = map[*types.Func]bool{fn: true}
	for f := range a.activeCalls {
		sub.activeCalls[f] = true
	}
	sub.state = newFlowState()
	sub.guardedParams = map[int]bool{}
	// 注意：保护状态按“新函数自己的形参下标”建立，不能直接继承外层下标
	// （两个函数签名不同）。沿链保护体现在实参求值得到 NonNil。

	if sig.Recv() != nil {
		rv := vNonNil()
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			rv = a.eval(sel.X, st)
		}
		sub.state.vars[objectKey(sig.Recv())] = rv
		if rv.Kind == ValNonNil {
			sub.guardedParams[0] = true
		}
	}
	for i := 0; i < sig.Params().Len(); i++ {
		v := vUnknown()
		pi := i
		if sig.Recv() != nil {
			pi = i + 1
		}
		if i < len(argVals) {
			v = argVals[i]
		}
		sub.state.vars[objectKey(sig.Params().At(i))] = v
		// 初值即确定非 nil（来自调用方判空分支）的形参视为受保护。
		if v.Kind == ValNonNil {
			sub.guardedParams[pi] = true
		}
	}
	r := sig.Results()
	sub.returnVals = make([]Val, r.Len())
	for i := 0; i < r.Len(); i++ {
		sub.resultObjs = append(sub.resultObjs, r.At(i))
		if r.At(i).Name() != "" {
			sub.state.vars[objectKey(r.At(i))] = zeroVal(r.At(i).Type())
		}
	}

	sub.walkBlock(decl.Body, sub.state)
	a.diags = append(a.diags, sub.diags...)
	a.ldiags = append(a.ldiags, sub.ldiags...)
	return true
}

// subAnalyzer 构造用于内联的子分析器，复用 resolver 并延伸调用路径。
func (a *intraAnalyzer) subAnalyzer(fn *types.Func, decl *ast.FuncDecl, call *ast.CallExpr) *intraAnalyzer {
	// 语义：CallPath 是“从根到当前函数调用点”的节点链（不含当前函数自身）。
	// 内联新函数 newfn 时，新调用方是当前函数：
	//   新 CallPath = 当前函数的 CallPath + 当前函数调用点节点
	var base []diag.CallNode
	if a.env.Caller != nil {
		base = append(base, a.env.Caller.CallPath...)
	}
	base = append(base, nodeOf(a.fset, a.env.Func, call.Pos()))
	// 传播调用链函数锚点，并加入新被调函数自身锚点。
	pstarts := map[string]fnStart{}
	for k, v := range a.pathStarts {
		pstarts[k] = v
	}
	np := a.fset.Position(decl.Name.Pos())
	pstarts[fn.FullName()] = fnStart{file: np.Filename, line: np.Line}

	return &intraAnalyzer{
		env: FuncEnv{
			Func:         fn,
			Decl:         decl,
			Fset:         a.fset,
			Info:         a.info,
			SendStates:   a.env.SendStates,
			ShowExcluded: a.emitExc,
			Caller: &Caller{
				Func:     a.env.Func,
				CallPos:  call.Pos(),
				CallPath: base,
			},
			Decls: a.declByFn,
		},
		resolve:     a.resolve,
		fset:        a.fset,
		info:        a.info,
		emitExc:     a.emitExc,
		derefParams: map[int]bool{},
		declByFn:    a.declByFn,
		pathStarts:  pstarts,
	}
}

// findDecl 在同包声明表中查找函数声明（内联只支持同包函数）。
func (a *intraAnalyzer) findDecl(fn *types.Func) *ast.FuncDecl {
	if a.declByFn != nil {
		return a.declByFn[fn]
	}
	return nil
}
