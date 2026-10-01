package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// intraAnalyzer 对单个函数做流敏感的过程内分析。
type intraAnalyzer struct {
	env     FuncEnv
	resolve Resolver
	// 调用路径：根函数在前，当前函数在末尾。
	callPath []diag.CallNode

	diags       []diag.Diagnostic
	derefParams map[int]bool      // 形参下标 -> 存在无保护解引用
	derefPos    map[int]token.Pos // 形参下标 -> 首个解引用位置

	state   *flowState
	ldiags  []locDiag
	fset    *token.FileSet
	info    *types.Info
	emitExc bool // 是否记录被保护排除的证据

	// 内联支持。
	activeCalls   map[*types.Func]bool
	declByFn      map[*types.Func]*ast.FuncDecl
	guardedParams map[int]bool       // 内联时调用方已判空保护的形参
	pathStarts    map[string]fnStart // 调用链各函数声明锚点（loc 用）

	// 当前函数结果：各 return 语句抽象值的合并。
	returnVals []Val
	hasReturn  bool
	// returnPaths 按出现顺序记录每条 return 语句的结果元组。
	returnPaths []ReturnPath
	// resultOk 记录命名返回值，用于裸 return。
	resultObjs []*types.Var
}

func (a *intraAnalyzer) run() (*Summary, []diag.Diagnostic) {
	a.fset = a.env.Fset
	a.info = a.env.Info
	a.derefParams = map[int]bool{}
	a.activeCalls = map[*types.Func]bool{a.env.Func: true}
	if a.pathStarts == nil {
		a.pathStarts = map[string]fnStart{}
	}
	if a.env.Decl != nil {
		pp := a.fset.Position(a.env.Decl.Name.Pos())
		a.pathStarts[a.env.Func.FullName()] = fnStart{file: pp.Filename, line: pp.Line}
	}
	a.emitExc = a.env.ShowExcluded
	a.state = newFlowState()

	sig, _ := a.env.Func.Type().(*types.Signature)
	// 接收者视为非 nil（显式 nil 接收者调用极少，避免误报）。
	if sig != nil && sig.Recv() != nil {
		a.state.vars[objectKey(sig.Recv())] = vNonNil()
	}
	// 形参初始为未知。
	if sig != nil {
		p := sig.Params()
		for i := 0; i < p.Len(); i++ {
			a.state.vars[objectKey(p.At(i))] = vUnknown()
		}
		// 命名返回值初始为 nil 语义（零值），记录以便裸 return。
		r := sig.Results()
		for i := 0; i < r.Len(); i++ {
			a.resultObjs = append(a.resultObjs, r.At(i))
			if r.At(i).Name() != "" {
				a.state.vars[objectKey(r.At(i))] = zeroVal(r.At(i).Type())
			}
		}
		a.returnVals = make([]Val, r.Len())
	}

	if a.env.Decl.Body != nil {
		a.walkBlock(a.env.Decl.Body, a.state)
	}

	results := a.returnVals
	if results == nil {
		results = []Val{}
	}
	paths := a.returnPaths
	// 无 return 语句的函数：若有返回值，按命名返回值/零值填充。
	if !a.hasReturn && sig != nil {
		r := sig.Results()
		results = make([]Val, r.Len())
		rp := ReturnPath{}
		for i := 0; i < r.Len(); i++ {
			if r.At(i).Name() != "" {
				results[i] = a.state.vars[objectKey(r.At(i))]
			} else {
				results[i] = zeroVal(r.At(i).Type())
			}
			v := results[i]
			v.Call = nil
			rp.Vals = append(rp.Vals, v)
			if isBoolType(r.At(i).Type()) {
				rp.Truths = append(rp.Truths, results[i].Truth)
			} else {
				rp.Truths = append(rp.Truths, TruthUnknown)
			}
		}
		paths = append(paths, rp)
	}
	var dp []int
	for i := range a.derefParams {
		dp = append(dp, i)
	}
	sum := &Summary{
		Func:        a.env.Func,
		Results:     results,
		Paths:       paths,
		DerefParams: dp,
		DerefPos:    a.derefPos,
	}
	return sum, a.diags
}

// walkBlock 顺序执行一组语句，返回语句块结束后的状态；nil 表示不可达/已终止。
func (a *intraAnalyzer) walkBlock(b *ast.BlockStmt, st *flowState) *flowState {
	cur := st
	for _, s := range b.List {
		if cur == nil {
			break
		}
		cur = a.walkStmt(s, cur)
	}
	return cur
}

func (a *intraAnalyzer) walkStmt(s ast.Stmt, st *flowState) *flowState {
	switch n := s.(type) {
	case *ast.BlockStmt:
		return a.walkBlock(n, st)
	case *ast.AssignStmt:
		return a.transferAssign(n, st)
	case *ast.DeclStmt:
		return a.transferDecl(n, st)
	case *ast.IfStmt:
		return a.walkIf(n, st)
	case *ast.ReturnStmt:
		return a.walkReturn(n, st)
	case *ast.ExprStmt:
		a.eval(n.X, st)
		return st
	case *ast.IncDecStmt:
		a.eval(n.X, st)
		return st
	case *ast.ForStmt:
		return a.walkFor(n, st)
	case *ast.RangeStmt:
		return a.walkRange(n, st)
	case *ast.SwitchStmt:
		return a.walkSwitch(n, st, nil)
	case *ast.TypeSwitchStmt:
		return a.walkTypeSwitch(n, st)
	case *ast.SelectStmt:
		return a.walkSelect(n, st)
	case *ast.GoStmt:
		a.eval(n.Call, st)
		return st
	case *ast.DeferStmt:
		// defer 体在函数返回时执行，按当前捕获环境单独分析，不影响主路径。
		a.analyzeDeferred(n.Call, st)
		return st
	case *ast.BranchStmt, *ast.EmptyStmt:
		return st
	case *ast.LabeledStmt:
		return a.walkStmt(n.Stmt, st)
	case *ast.SendStmt:
		cv := a.eval(n.Chan, st)
		a.eval(n.Value, st)
		if a.env.SendStates != nil {
			a.env.SendStates[n.Pos()] = cv.Kind
		}
		return st
	default:
		// 未显式建模的语句：保守地不求值其内部解引用之外的信息。
		return st
	}
}

func (a *intraAnalyzer) walkIf(n *ast.IfStmt, st *flowState) *flowState {
	if n.Init != nil {
		st = a.walkStmt(n.Init, st)
	}
	thenSt := refineOnCond(st.clone(), n.Cond, true, a.info)
	thenSt = a.walkBlock(n.Body, thenSt)
	var elseSt *flowState
	if n.Else != nil {
		elseSt = refineOnCond(st.clone(), n.Cond, false, a.info)
		elseSt = a.walkStmt(n.Else, elseSt)
	} else {
		elseSt = refineOnCond(st.clone(), n.Cond, false, a.info)
	}
	return mergeStates(thenSt, elseSt)
}

func (a *intraAnalyzer) walkFor(n *ast.ForStmt, st *flowState) *flowState {
	if n.Init != nil {
		st = a.walkStmt(n.Init, st)
	}
	// 求值条件以触发解引用检查。
	if n.Cond != nil {
		a.eval(n.Cond, st)
	}
	bodySt := st.clone()
	if n.Cond != nil {
		bodySt = refineOnCond(bodySt, n.Cond, true, a.info)
	}
	bodySt = a.walkBlock(n.Body, bodySt)
	if n.Post != nil {
		bodySt = a.walkStmt(n.Post, bodySt)
	}
	// 合并循环入口与回边，再与退出分支合并（保守）。
	loop := mergeStates(st, bodySt)
	out := st.clone()
	if n.Cond != nil {
		out = refineOnCond(out, n.Cond, false, a.info)
	}
	return mergeStates(loop, out)
}

func (a *intraAnalyzer) walkRange(n *ast.RangeStmt, st *flowState) *flowState {
	a.eval(n.X, st)
	bodySt := st.clone()
	if key, ok := n.Key.(*ast.Ident); ok && key.Name != "_" {
		bodySt.vars[a.objKeyOf(key)] = vUnknown()
	}
	if val, ok := n.Value.(*ast.Ident); ok && val.Name != "_" {
		bodySt.vars[a.objKeyOf(val)] = vUnknown()
	}
	bodySt = a.walkBlock(n.Body, bodySt)
	return mergeStates(st, bodySt)
}

func (a *intraAnalyzer) walkSwitch(n *ast.SwitchStmt, st *flowState, tag ast.Expr) *flowState {
	if n.Init != nil {
		st = a.walkStmt(n.Init, st)
	}
	if n.Tag != nil {
		a.eval(n.Tag, st)
	}
	var ends []*flowState
	var defaultBody *flowState
	for _, c := range n.Body.List {
		cc := c.(*ast.CaseClause)
		for _, cond := range cc.List {
			a.eval(cond, st)
		}
		branch := st.clone()
		// case 分支本身不直接给出 nil 精化（tag == v 不常见地涉及 nil 判定），
		// 但 `case nil:` 可在条件精化中被利用，这里保守不细化。
		end := a.walkStmtList(cc.Body, branch)
		if end != nil {
			if cc.List == nil {
				defaultBody = end
			} else {
				ends = append(ends, end)
			}
		}
	}
	out := mergeStateList(ends)
	if defaultBody != nil {
		out = mergeStates(out, defaultBody)
	}
	if out == nil {
		out = st
	}
	return out
}

func (a *intraAnalyzer) walkTypeSwitch(n *ast.TypeSwitchStmt, st *flowState) *flowState {
	if n.Init != nil {
		st = a.walkStmt(n.Init, st)
	}
	if as, ok := n.Assign.(*ast.AssignStmt); ok {
		st = a.walkStmt(as, st)
	}
	var ends []*flowState
	for _, c := range n.Body.List {
		cc := c.(*ast.CaseClause)
		branch := st.clone()
		end := a.walkStmtList(cc.Body, branch)
		if end != nil {
			ends = append(ends, end)
		}
	}
	if out := mergeStateList(ends); out != nil {
		return out
	}
	return st
}

func (a *intraAnalyzer) walkSelect(n *ast.SelectStmt, st *flowState) *flowState {
	var ends []*flowState
	for _, c := range n.Body.List {
		cc := c.(*ast.CommClause)
		branch := st.clone()
		if cc.Comm != nil {
			branch = a.walkStmt(cc.Comm, branch)
		}
		if branch != nil {
			end := a.walkStmtList(cc.Body, branch)
			if end != nil {
				ends = append(ends, end)
			}
		}
	}
	if out := mergeStateList(ends); out != nil {
		return out
	}
	return st
}

func (a *intraAnalyzer) walkStmtList(list []ast.Stmt, st *flowState) *flowState {
	cur := st
	for _, s := range list {
		if cur == nil {
			break
		}
		cur = a.walkStmt(s, cur)
	}
	return cur
}

func (a *intraAnalyzer) walkReturn(n *ast.ReturnStmt, st *flowState) *flowState {
	if len(n.Results) == 0 {
		// 裸 return：取命名返回值当前状态。
		vals := make([]Val, len(a.resultObjs))
		for i, v := range a.resultObjs {
			vals[i] = st.vars[objectKey(v)]
			a.mergeReturn(i, vals[i])
		}
		a.recordReturn(vals, nil, n.Pos(), st)
		a.hasReturn = true
		return nil
	}
	if len(n.Results) == 1 {
		// 可能是多值调用返回。
		if vals, ok := a.evalMulti(n.Results[0], st); ok {
			for i, v := range vals {
				a.mergeReturn(i, v)
			}
			a.recordMultiReturn(vals, n.Results[0], n.Pos(), st)
			a.hasReturn = true
			return nil
		}
	}
	vals := make([]Val, len(n.Results))
	for i, e := range n.Results {
		vals[i] = a.eval(e, st)
		a.mergeReturn(i, vals[i])
	}
	a.recordReturn(vals, n.Results, n.Pos(), st)
	a.hasReturn = true
	return nil
}

// recordMultiReturn 处理单表达式多值返回（f() / m[k] / v.(T) / <-ch）。
// 函数调用的真假来自被调摘要路径；内置二元形式的 ok 真假无法从表达式本身
// 得到（语法里没有真假字面量），按未知处理——其值也是 Unknown，不误报。
func (a *intraAnalyzer) recordMultiReturn(vals []Val, e ast.Expr, pos token.Pos, st *flowState) {
	sig, _ := a.env.Func.Type().(*types.Signature)
	if sig == nil {
		return
	}
	r := sig.Results()
	rp := ReturnPath{Pos: pos}
	truthByIdx := map[int]Truth{}
	if call, ok := e.(*ast.CallExpr); ok {
		if fn := calledFunc(call, a.info); fn != nil && a.resolve != nil {
			if sum := a.resolve(fn); sum != nil {
				for _, p := range sum.Paths {
					for i, t := range p.Truths {
						if t == TruthUnknown {
							continue
						}
						if cur, exists := truthByIdx[i]; !exists {
							truthByIdx[i] = t
						} else if cur != t {
							truthByIdx[i] = TruthUnknown
						}
					}
				}
			}
		}
	}
	for i := 0; i < r.Len(); i++ {
		var v Val
		if i < len(vals) {
			v = vals[i]
		} else {
			v = zeroVal(r.At(i).Type())
		}
		v.Call = nil
		rp.Vals = append(rp.Vals, v)
		if isBoolType(r.At(i).Type()) {
			t := truthByIdx[i]
			if t == TruthUnknown {
				t = vals[i].Truth
			}
			rp.Truths = append(rp.Truths, t)
		} else {
			rp.Truths = append(rp.Truths, TruthUnknown)
		}
	}
	a.returnPaths = append(a.returnPaths, rp)
}

// recordReturn 保存一条 return 路径的结果空值元组与布尔结果真假性，
// 供调用点做 (data, error)/(data, ok) 的路径相关精化。
func (a *intraAnalyzer) recordReturn(vals []Val, exprs []ast.Expr, pos token.Pos, st *flowState) {
	sig, _ := a.env.Func.Type().(*types.Signature)
	if sig == nil {
		return
	}
	r := sig.Results()
	rp := ReturnPath{Pos: pos}
	for i := 0; i < r.Len(); i++ {
		var v Val
		if i < len(vals) {
			v = vals[i]
		} else {
			v = zeroVal(r.At(i).Type())
		}
		// 摘要中的值不携带调用出处（出处属于调用点的关联）。
		v.Call = nil
		rp.Vals = append(rp.Vals, v)
		if isBoolType(r.At(i).Type()) {
			t := TruthUnknown
			if len(exprs) == 1 {
				// 多值返回：若被调摘要也带路径信息，truthOf 无法跨调用解析，
				// 此时保持未知（evalCallMulti 会另行传递，见 branch 精化回退）。
				t = TruthUnknown
			} else if i < len(exprs) {
				t = a.truthOf(exprs[i], st)
			}
			if t == TruthUnknown {
				t = v.Truth
			}
			rp.Truths = append(rp.Truths, t)
		} else {
			rp.Truths = append(rp.Truths, TruthUnknown)
		}
	}
	a.returnPaths = append(a.returnPaths, rp)
}

func (a *intraAnalyzer) mergeReturn(i int, v Val) {
	if len(a.returnVals) <= i {
		extended := make([]Val, i+1)
		copy(extended, a.returnVals)
		a.returnVals = extended
	}
	// 摘要层面不保留调用出处：CallRef 是调用点局部的语法关联。
	v.Call = nil
	if !a.hasReturn {
		a.returnVals[i] = v
	} else {
		a.returnVals[i] = joinVal(a.returnVals[i], v)
	}
}

func (a *intraAnalyzer) analyzeDeferred(call *ast.CallExpr, st *flowState) {
	fn := calledFunc(call, a.info)
	if fn == nil {
		return
	}
	// defer 常见形式：defer mu.Unlock()；空值上按调用求值即可。
	a.eval(call, st)
}

func mergeStates(a, b *flowState) *flowState {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return a.join(b)
}

func mergeStateList(ss []*flowState) *flowState {
	var m *flowState
	for _, s := range ss {
		m = mergeStates(m, s)
	}
	return m
}

func objectKey(o types.Object) interface{} { return o }

func (a *intraAnalyzer) objKeyOf(id *ast.Ident) interface{} {
	if obj := a.info.ObjectOf(id); obj != nil {
		return objectKey(obj)
	}
	return id
}

func zeroVal(t types.Type) Val {
	// 指针/切片/map/chan/func/interface 零值为 nil；具名类型按底层类型判定。
	u := t
	if n, ok := t.(*types.Named); ok {
		u = n.Underlying()
	}
	switch u.(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan,
		*types.Signature, *types.Interface, *types.TypeParam:
		return vNil()
	}
	return vNonNil()
}

// paramGuardKind 返回形参的保护状态。
func (a *intraAnalyzer) paramGuardKind(target ast.Expr) (int, bool) {
	id := rootIdent(target)
	if id == nil {
		return 0, false
	}
	par, ok := a.info.ObjectOf(id).(*types.Var)
	if !ok {
		return 0, false
	}
	sig, _ := a.env.Func.Type().(*types.Signature)
	if sig == nil {
		return 0, false
	}
	idx := -1
	if sig.Recv() == par {
		idx = 0
	} else {
		for i := 0; i < sig.Params().Len(); i++ {
			if sig.Params().At(i) == par {
				idx = i
				if sig.Recv() != nil {
					idx++
				}
				break
			}
		}
	}
	if idx < 0 || a.guardedParams == nil {
		return 0, false
	}
	return idx, a.guardedParams[idx]
}
