// Package loader 负责解析源文件、收集类型信息并维护按文件缓存。
//
// 增量策略：Loader 持有一个持久的 token.FileSet。内容哈希未变化的文件
// 复用已解析的 AST 与其中的 token.Pos；变化的文件重新解析，并在同一
// FileSet 中注册新的位置区间（旧区间不再被任何 AST 引用，仅历史遗留）。
package loader

import (
	"crypto/sha1"
	"encoding/hex"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
)

// InputFile 是一份待分析的源文件（路径 + 内容）。
type InputFile struct {
	Path    string
	Content string
}

// fileEntry 是单个文件的缓存条目。
type fileEntry struct {
	hash string
	file *ast.File
	src  string
}

// FileInfo 保存单个文件解析后的信息。
type FileInfo struct {
	Path string
	Hash string
	File *ast.File
	Fset *token.FileSet
}

// PkgInfo 保存一个包的解析与类型检查结果。
type PkgInfo struct {
	PkgPath  string
	Dir      string
	Name     string
	Fset     *token.FileSet
	Files    map[string]*FileInfo
	Ordered  []*FileInfo
	TypeInfo *types.Info
	Pkg      *types.Package
}

// ChangedFiles 描述本次 Load 相对上一次的差异。
type ChangedFiles struct {
	Changed []string // 新增或内容变化
	Deleted []string // 不再出现在输入集合中
}

// Result 是一次加载的结果。
type Result struct {
	Fset    *token.FileSet
	Pkgs    map[string]*PkgInfo
	Changed ChangedFiles
}

// Source 返回某文件最近一次解析使用的源码。
func (l *Loader) Source(path string) string {
	if e, ok := l.entries[path]; ok {
		return e.src
	}
	return ""
}

// Loader 维护文件哈希以检测变化；每轮在全新 FileSet 中重新解析。
type Loader struct {
	entries map[string]*fileEntry
}

func New() *Loader {
	return &Loader{entries: map[string]*fileEntry{}}
}

func hashContent(src string) string {
	sum := sha1.Sum([]byte(src))
	return hex.EncodeToString(sum[:])
}

// Load 接收当前完整文件集合：
//   - 每次使用全新的 token.FileSet 重新解析并类型检查全部文件，保证所有
//     token.Pos 都相对本次 FileSet 一致（跨轮次复用 AST 会让缓存中的位置
//     指向失效区间）；AST 解析本身很廉价。
//   - 文件级哈希仅用于检测“哪些文件内容发生了变化”，供上层做函数级增量。
func (l *Loader) Load(files []InputFile) (*Result, error) {
	fset := token.NewFileSet()
	res := &Result{Fset: fset, Pkgs: map[string]*PkgInfo{}}
	seen := map[string]bool{}
	newEntries := map[string]*fileEntry{}

	// 先检测变化（相对上一次的哈希），再在新 FileSet 中解析全部文件。
	for _, in := range files {
		seen[in.Path] = true
		h := hashContent(in.Content)
		if old, ok := l.entries[in.Path]; !ok || old.hash != h {
			res.Changed.Changed = append(res.Changed.Changed, in.Path)
		}
		f, err := parser.ParseFile(fset, in.Path, in.Content, parser.ParseComments|parser.AllErrors)
		if err != nil && f == nil {
			return nil, err
		}
		newEntries[in.Path] = &fileEntry{hash: h, file: f, src: in.Content}
	}
	for p := range l.entries {
		if !seen[p] {
			res.Changed.Deleted = append(res.Changed.Deleted, p)
		}
	}
	sort.Strings(res.Changed.Changed)
	sort.Strings(res.Changed.Deleted)
	l.entries = newEntries

	// 按目录 + 包名分组成包。
	groups := map[string][]InputFile{}
	var groupKeys []string
	for _, in := range files {
		e := l.entries[in.Path]
		key := dirOf(in.Path) + "\x00" + e.file.Name.Name
		if _, ok := groups[key]; !ok {
			groupKeys = append(groupKeys, key)
		}
		groups[key] = append(groups[key], in)
	}
	sort.Strings(groupKeys)

	for _, key := range groupKeys {
		ins := groups[key]
		sort.Slice(ins, func(i, j int) bool { return ins[i].Path < ins[j].Path })
		pi := &PkgInfo{
			Fset:     fset,
			Files:    map[string]*FileInfo{},
			TypeInfo: newTypeInfo(),
		}
		var astFiles []*ast.File
		for _, in := range ins {
			e := l.entries[in.Path]
			fi := &FileInfo{Path: in.Path, Hash: e.hash, File: e.file, Fset: fset}
			pi.Files[in.Path] = fi
			pi.Ordered = append(pi.Ordered, fi)
			astFiles = append(astFiles, e.file)
			pi.Dir = dirOf(in.Path)
			pi.Name = e.file.Name.Name
		}
		pi.PkgPath = pi.Dir
		if pi.PkgPath == "" {
			pi.PkgPath = pi.Name
		}
		l.typeCheck(fset, pi, astFiles)
		res.Pkgs[pi.PkgPath] = pi
	}
	return res, nil
}

func newTypeInfo() *types.Info {
	return &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
}

func (l *Loader) typeCheck(fset *token.FileSet, pi *PkgInfo, files []*ast.File) {
	conf := types.Config{
		Importer: importer.Default(),
		Error:    func(err error) { /* 容忍部分错误，尽力收集类型信息 */ },
	}
	pkg, _ := conf.Check(pi.PkgPath, fset, files, pi.TypeInfo)
	pi.Pkg = pkg
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}
