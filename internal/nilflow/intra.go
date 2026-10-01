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
	// returnTuples 记录每条 return 路径的完整返回值元组（保留返回值之间的关联），
	// returnExprs 为对应的结果表达式（裸 return 时为 nil）。
	returnTuples [][]Val
	returnExprs  [][]ast.Expr
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
	// 无 return 语句的函数：若有返回值，按命名返回值/零值填充。
	if !a.hasReturn && sig != nil {
		r := sig.Results()
		results = make([]Val, r.Len())
		for i := 0; i < r.Len(); i++ {
			if r.At(i).Name() != "" {
				results[i] = a.state.vars[objectKey(r.At(i))]
			} else {
				results[i] = zeroVal(r.At(i).Type())
			}
		}
	}
	var dp []int
	for i := range a.derefParams {
		dp = append(dp, i)
	}
	sum := &Summary{
		Func:        a.env.Func,
		Results:     results,
		SiblingCond: a.buildSiblingConds(sig),
		DerefParams: dp,
		DerefPos:    a.derefPos,
	}
	return sum, a.diags
}

// buildSiblingConds 汇总所有 return 路径，构建“error/ok 兄弟返回值取某真值时，
// 数据返回值的空值”这一条件关联。只有全部可达路径被考虑，结论才不会被强化。
func (a *intraAnalyzer) buildSiblingConds(sig *types.Signature) []SiblingCond {
	if sig == nil || len(a.returnTuples) == 0 {
		return nil
	}
	r := sig.Results()
	n := r.Len()
	if n < 2 {
		return nil
	}
	// 找出作为条件的兄弟下标：error 或 bool。
	type sib struct {
		idx   int
		isErr bool
	}
	var sibs []sib
	for i := 0; i < n; i++ {
		u := r.At(i).Type()
		if named, ok := u.(*types.Named); ok {
			if obj := named.Obj(); obj != nil && obj.Pkg() == nil && obj.Name() == "error" {
				sibs = append(sibs, sib{idx: i, isErr: true})
				continue
			}
		}
		if b, ok := u.Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
			sibs = append(sibs, sib{idx: i, isErr: false})
		}
	}
	if len(sibs) == 0 {
		return nil
	}
	var conds []SiblingCond
	for _, sb := range sibs {
		for di := 0; di < n; di++ {
			if di == sb.idx {
				continue
			}
			// 仅对可能为 nil 的数据结果建立关联（标量类型解引用无意义）。
			if !nillableType(r.At(di).Type()) {
				continue
			}
			c := SiblingCond{ResultIdx: di, SibIdx: sb.idx, SibIsErr: sb.isErr}
			active := false
			for ti, tuple := range a.returnTuples {
				if di >= len(tuple) || sb.idx >= len(tuple) {
					continue
				}
				dv := stripCall(tuple[di])
				t, f := a.returnSibTruth(tuple, a.returnExprs[ti], sb.idx)
				if t {
					c.addToBucket(true, dv)
					c.HasTrue = true
					active = true
				}
				if f {
					c.addToBucket(false, dv)
					c.HasFalse = true
					active = true
				}
			}
			if active {
				c.finishBuckets()
				conds = append(conds, c)
			}
		}
	}
	return conds
}

// returnSibTruth 判断单条 return 路径上兄弟返回值的真假归属。
// 返回 (canBeTrue, canBeFalse)；无法确定时两者皆真（保守覆盖两侧）。
func (a *intraAnalyzer) returnSibTruth(tuple []Val, exprs []ast.Expr, sibIdx int) (bool, bool) {
	v := tuple[sibIdx]
	// error 结果的空值格信息最可靠：显式 nil/非 nil 字面量直接决定真假。
	if sibIsError(a.env.Func, sibIdx) {
		switch v.Kind {
		case ValNil:
			return false, true
		case ValNonNil:
			return true, false
		}
	}
	// 多值透传调用：直接采用被调函数给出的条件结构。
	if len(exprs) == 1 && sibIdx < len(tuple) {
		if ref := tuple[sibIdx].Call; ref != nil && ref.Cond != nil {
			return ref.Cond.HasTrue, ref.Cond.HasFalse
		}
	}
	var e ast.Expr
	if sibIdx < len(exprs) {
		e = exprs[sibIdx]
	}
	if e != nil {
		switch tv := staticBoolTruth(e, a.info); tv {
		case 1:
			return true, false
		case -1:
			return false, true
		}
	}
	// bool 结果的空值格不表达真假（bool 非 nil 类型时甚至不进格），
	// 无法确定时保守覆盖两侧。
	return true, true
}

// sibIsError 判断某函数结果下标是否为内建 error 类型。
func sibIsError(fn *types.Func, idx int) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	r := sig.Results()
	if idx >= r.Len() {
		return false
	}
	named, ok := r.At(idx).Type().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() == nil && obj.Name() == "error"
}

// addToBucket 把一条可达返回路径上的数据空值并入指定真值桶。
func (c *SiblingCond) addToBucket(truth bool, v Val) {
	if truth {
		if !c.HasTrue {
			c.WhenTrue = v
		} else {
			c.WhenTrue = joinVal(c.WhenTrue, v)
		}
		if v.Kind == ValNil {
			c.trueNil = true
		}
		return
	}
	if !c.HasFalse {
		c.WhenFalse = v
	} else {
		c.WhenFalse = joinVal(c.WhenFalse, v)
	}
	if v.Kind == ValNil {
		c.falseNil = true
	}
}

// finishBuckets 在汇总全部可达返回路径后收尾：
//   - 不可达真值桶：error!=nil 不可达时标记非 nil（该分支不会进入，避免
//     失败保护后误报）；其余不可达桶保持未知；
//   - error!=nil 桶仅因混入来源未知（Unknown）而成为 MaybeNil、且没有任何
//     确定 nil 失败路径时，收敛为非 nil；存在真实 nil 失败路径则保留
//     （失败分支内的解引用仍须报告）。
func (c *SiblingCond) finishBuckets() {
	if !c.HasTrue {
		if c.SibIsErr {
			c.WhenTrue = Val{Kind: ValNonNil}
		} else {
			c.WhenTrue = Val{Kind: ValUnknown}
		}
	}
	if !c.HasFalse {
		if c.SibIsErr {
			c.WhenFalse = Val{Kind: ValNonNil}
		} else {
			c.WhenFalse = Val{Kind: ValUnknown}
		}
	}
	if c.SibIsErr && c.HasTrue && c.WhenTrue.Kind == ValMaybeNil && !c.trueNil {
		c.WhenTrue = Val{Kind: ValNonNil}
	}
}

// stripCall 返回去掉调用出处的空值（摘要内不保留跨轮 AST 指针）。
func stripCall(v Val) Val { return Val{Kind: v.Kind} }

// nillableType 判断类型的零值/取值是否可能为 nil。
func nillableType(t types.Type) bool {
	u := t
	if n, ok := t.(*types.Named); ok {
		u = n.Underlying()
	}
	switch u.(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan,
		*types.Signature, *types.Interface, *types.TypeParam:
		return true
	}
	return false
}

// staticBoolTruth 静态求值布尔常量表达式：1 恒真、-1 恒假、0 未知。
func staticBoolTruth(e ast.Expr, info *types.Info) int {
	switch n := e.(type) {
	case *ast.Ident:
		switch n.Name {
		case "true":
			return 1
		case "false":
			return -1
		}
	case *ast.ParenExpr:
		return staticBoolTruth(n.X, info)
	case *ast.UnaryExpr:
		if n.Op == token.NOT {
			if t := staticBoolTruth(n.X, info); t != 0 {
				return -t
			}
		}
	}
	return 0
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
	// then/else 必须从条件前的同一状态各自独立精化派生：
	// else 分支不能建立在 then 执行后的状态上，否则会把 then 分支对调用返回值
	// 的条件精化错误带进 else（例如失败分支里残留成功分支的数据空值）。
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
			a.mergeReturn(i, st.vars[objectKey(v)])
		}
		a.recordReturn(vals, nil)
		a.hasReturn = true
		return nil
	}
	if len(n.Results) == 1 {
		// 可能是多值调用返回。
		if vals, ok := a.evalMulti(n.Results[0], st); ok {
			for i, v := range vals {
				a.mergeReturn(i, v)
			}
			exprs := make([]ast.Expr, len(vals))
			for i := range vals {
				exprs[i] = multiResultExpr(n.Results[0], i)
			}
			a.recordReturn(vals, exprs)
			a.hasReturn = true
			return nil
		}
	}
	vals := make([]Val, len(n.Results))
	for i, e := range n.Results {
		vals[i] = a.eval(e, st)
		a.mergeReturn(i, vals[i])
	}
	a.recordReturn(vals, n.Results)
	a.hasReturn = true
	return nil
}

// recordReturn 保存一条返回路径上的完整结果元组。
func (a *intraAnalyzer) recordReturn(vals []Val, exprs []ast.Expr) {
	a.returnTuples = append(a.returnTuples, append([]Val(nil), vals...))
	a.returnExprs = append(a.returnExprs, append([]ast.Expr(nil), exprs...))
}

// multiResultExpr 返回多值表达式第 idx 个结果对应的表达式，供返回值真值判定。
// 对调用表达式直接复用调用节点（真实真假取自摘要条件）；其余内建多值形式
// （map 索引/类型断言/通道接收）结果真假无法静态确定，返回 nil。
func multiResultExpr(e ast.Expr, idx int) ast.Expr {
	if idx != 1 {
		// 主结果通常不是 bool/error；调用场景仍由元组 Call 条件处理。
		if _, ok := e.(*ast.CallExpr); ok {
			return e
		}
		return nil
	}
	if _, ok := e.(*ast.CallExpr); ok {
		return e
	}
	return nil
}

func (a *intraAnalyzer) mergeReturn(i int, v Val) {
	if len(a.returnVals) <= i {
		extended := make([]Val, i+1)
		copy(extended, a.returnVals)
		a.returnVals = extended
	}
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
