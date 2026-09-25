package analysis

import (
	"go/ast"
	"go/types"
	"sort"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// indexFunctions registers every named local function/method with a body and
// resolves static callee edges.
func (p *Program) indexFunctions() {
	paths := make([]string, 0, len(p.pkgs))
	for path := range p.pkgs {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		pkg := p.pkgs[path]
		for _, fmeta := range pkg.Files {
			for _, decl := range fmeta.Syntax.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				obj, _ := pkg.Info.Defs[fn.Name].(*types.Func)
				if obj == nil {
					continue
				}
				sig, _ := obj.Type().(*types.Signature)
				if sig == nil {
					continue
				}
				fd := &funcDecl{
					obj:     obj,
					sig:     sig,
					decl:    fn,
					pkg:     pkg,
					pkgPath: path,
				}
				if params := sig.Params(); params != nil {
					for i := 0; i < params.Len(); i++ {
						fd.params = append(fd.params, params.At(i))
					}
				}
				if results := sig.Results(); results != nil {
					for i := 0; i < results.Len(); i++ {
						fd.results = append(fd.results, results.At(i))
					}
				}
				p.funcs[obj] = fd
				p.byDecl[fn] = fd
			}
		}
	}

	// Resolve edges in the same deterministic order.
	seen := map[*funcDecl]bool{}
	for _, path := range paths {
		pkg := p.pkgs[path]
		for _, fmeta := range pkg.Files {
			for _, decl := range fmeta.Syntax.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				fd := p.byDecl[fn]
				if fd == nil {
					continue
				}
				seen = map[*funcDecl]bool{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if callee := p.resolveCallee(pkg, call); callee != nil && !seen[callee] {
						seen[callee] = true
						fd.edges = append(fd.edges, callee)
					}
					return true
				})
			}
		}
	}
}

// resolveCallee maps a call expression to an indexed local function when the
// callee is statically resolvable (including concrete method values).
func (p *Program) resolveCallee(pkg *loader.Package, call *ast.CallExpr) *funcDecl {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj = pkg.Info.Uses[fun]
	case *ast.SelectorExpr:
		if pkg.Info == nil {
			return nil
		}
		if sel := pkg.Info.Selections[fun]; sel != nil && sel.Kind() == types.MethodVal {
			obj = sel.Obj()
		} else if u := pkg.Info.Uses[fun.Sel]; u != nil {
			obj = u
		}
	}
	if obj == nil {
		return nil
	}
	if fn, ok := obj.(*types.Func); ok {
		return p.funcs[fn]
	}
	return nil
}

// calleeAt resolves a call to an indexed function, or nil for external /
// dynamic calls.
func (p *Program) calleeAt(pkg *loader.Package, call *ast.CallExpr) *funcDecl {
	return p.resolveCallee(pkg, call)
}
