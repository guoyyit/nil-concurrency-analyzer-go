package nilflow

import (
	"crypto/sha1"
	"encoding/hex"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

func collectFunctions(pi *loader.PkgInfo) []*fnEntry {
	var out []*fnEntry
	for _, fi := range pi.Ordered {
		for _, decl := range fi.File.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := fnOfDecl(fd, pi.TypeInfo)
			if fn == nil {
				continue
			}
			out = append(out, &fnEntry{key: funcKey(fn), fn: fn, decl: fd})
		}
	}
	return out
}

func fnOfDecl(fd *ast.FuncDecl, info *types.Info) *types.Func {
	if info == nil || info.Defs == nil {
		return nil
	}
	obj := info.Defs[fd.Name]
	if obj == nil {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}

// HashFuncDecl 返回函数体位置无关的稳定哈希。
func HashFuncDecl(fd *ast.FuncDecl) string {
	h := sha1.New()
	// 用空 FileSet 打印 AST 结构，哈希只依赖函数源码的语法内容。
	var pcfg printer.Config
	if err := pcfg.Fprint(h, token.NewFileSet(), fd); err != nil {
		// 退化方案：遍历标识符与字面量。
		ast.Inspect(fd, func(n ast.Node) bool {
			switch t := n.(type) {
			case *ast.Ident:
				h.Write([]byte(t.Name))
			case *ast.BasicLit:
				h.Write([]byte(t.Value))
			}
			return true
		})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callees 返回函数体内直接调用到的本包函数。
func callees(fd *ast.FuncDecl, info *types.Info) []*types.Func {
	var out []*types.Func
	seen := map[*types.Func]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn := calledFunc(call, info); fn != nil && !seen[fn] {
			seen[fn] = true
			out = append(out, fn)
		}
		return true
	})
	return out
}

// computeClosureHashes 计算 h(f)=hash(body(f), 排序后各 callee 的 h)。
func computeClosureHashes(entries []*fnEntry, pi *loader.PkgInfo) map[string]string {
	body := map[string]string{}
	neighbors := map[string][]*types.Func{}
	for _, e := range entries {
		body[e.key] = HashFuncDecl(e.decl)
		neighbors[e.key] = callees(e.decl, pi.TypeInfo)
	}
	res := map[string]string{}
	// 迭代到不动点（递归依赖时给未知 callee 固定占位，保证终止）。
	changed := true
	for i := 0; changed && i < 64; i++ {
		changed = false
		// 固定顺序。
		for _, e := range entries {
			h := sha1.New()
			h.Write([]byte(body[e.key]))
			cs := append([]*types.Func(nil), neighbors[e.key]...)
			// 去重并按全限定名排序。
			uniq := map[*types.Func]bool{}
			var names []string
			for _, c := range cs {
				if uniq[c] {
					continue
				}
				uniq[c] = true
				names = append(names, c.FullName())
			}
			sortStrings(names)
			for _, nm := range names {
				h.Write([]byte(nm))
				if v, ok := res[nm]; ok {
					h.Write([]byte(v))
				} else {
					h.Write([]byte("?"))
				}
			}
			nh := hex.EncodeToString(h.Sum(nil))
			if res[e.key] != nh {
				res[e.key] = nh
				changed = true
			}
		}
	}
	return res
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func summaryEqual(a, b *Summary) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Results {
		if a.Results[i].Kind != b.Results[i].Kind {
			return false
		}
	}
	if len(a.DerefParams) != len(b.DerefParams) {
		return false
	}
	am := map[int]bool{}
	for _, p := range a.DerefParams {
		am[p] = true
	}
	for _, p := range b.DerefParams {
		if !am[p] {
			return false
		}
	}
	return true
}

// anchorLines 从本轮包 AST 收集“函数全名 -> 声明锚点(文件,起始行)”，
// 供缓存诊断在全新 FileSet 上重定位。
func anchorLines(pi *loader.PkgInfo) map[string]fnStart {
	m := map[string]fnStart{}
	for _, e := range collectFunctions(pi) {
		p := pi.Fset.Position(e.decl.Name.Pos())
		m[e.key] = fnStart{file: p.Filename, line: p.Line}
	}
	return m
}
