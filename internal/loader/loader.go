// Package loader discovers Go packages under a module root, parses them and
// type-checks them with go/types using a custom importer that resolves the
// module's own packages in topological order.
//
// Parsed syntax trees and type-checked packages are cached so that an
// incremental driver can invalidate individual files/packages.
package loader

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
)

// FilePos is a 1-based source position in a specific file.
type FilePos struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// String renders "file:line:column".
func (p FilePos) String() string {
	return filepath.ToSlash(p.File) + ":" + itoa(p.Line) + ":" + itoa(p.Column)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// PosOf converts a token.Pos inside fset into a FilePos. The zero position
// yields the zero FilePos.
func PosOf(fset *token.FileSet, pos token.Pos) FilePos {
	if !pos.IsValid() {
		return FilePos{}
	}
	p := fset.Position(pos)
	return FilePos{File: p.Filename, Line: p.Line, Column: p.Column}
}

// HashBytes returns a stable short content hash.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// HashFile returns the content hash of a file.
func HashFile(abs string) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}

// File is one parsed Go source file.
type File struct {
	AbsPath string
	RelPath string // slash-separated, relative to module root
	Hash    string
	Syntax  *ast.File
}

// Meta describes a discovered package before type checking.
type Meta struct {
	ImportPath string
	Name       string
	Dir        string // absolute
	RelDir     string // slash-separated relative directory ("." for root)
	Files      []*File
	LocalDeps  []string // import paths belonging to this module, sorted, unique
	CheckError error    // last type-check error, if any
	metaFset   *token.FileSet
}

// Package is a parsed and type-checked Go package.
type Package struct {
	*Meta
	Types *types.Package
	Info  *types.Info
}

// GoFiles returns the absolute file paths of the package.
func (p *Package) GoFiles() []string {
	out := make([]string, len(p.Files))
	for i, f := range p.Files {
		out[i] = f.AbsPath
	}
	return out
}

// Syntax returns the parsed files of the package.
func (p *Package) Syntax() []*ast.File {
	out := make([]*ast.File, len(p.Files))
	for i, f := range p.Files {
		out[i] = f.Syntax
	}
	return out
}

// Dependencies returns the module-local import dependencies.
func (p *Package) Dependencies() []string { return p.LocalDeps }

// Fset returns the token FileSet used to parse the package.
func (p *Package) Fset() *token.FileSet { return p.metaFset }

// skipDir reports whether a directory name is ignored during scanning,
// following the go tool conventions plus testdata/vendor.
func skipDir(name string) bool {
	return name == "" || name[0] == '.' || name[0] == '_' ||
		name == "vendor" || name == "testdata" || name == "node_modules"
}

// skipFile reports whether a file name is ignored.
func skipFile(name string) bool {
	if !strings.HasSuffix(name, ".go") {
		return true
	}
	if strings.HasSuffix(name, "_test.go") {
		return true
	}
	return name[0] == '.' || name[0] == '_'
}

// Loader scans, parses and type-checks one Go module.
type Loader struct {
	root       string // absolute module root
	modulePath string
	fset       *token.FileSet
	cfg        *config.Config

	metas    map[string]*Meta    // import path -> meta
	pkgFiles map[string]string   // abs file path -> import path
	checked  map[string]*Package // successfully/partially checked
	parsed   map[string]*File    // abs path -> parsed file cache
	fallback types.Importer
}

// New creates a Loader rooted at dir (a directory containing go.mod or any
// directory; without go.mod the module path is "example.com/root").
func New(dir string, cfg *config.Config) (*Loader, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	l := &Loader{
		root:       abs,
		modulePath: readModulePath(abs),
		fset:       token.NewFileSet(),
		cfg:        cfg,
		metas:      map[string]*Meta{},
		pkgFiles:   map[string]string{},
		checked:    map[string]*Package{},
		parsed:     map[string]*File{},
		fallback:   importer.Default(),
	}
	return l, nil
}

// Root returns the absolute module root.
func (l *Loader) Root() string { return l.root }

// Fset returns the shared token file set.
func (l *Loader) Fset() *token.FileSet { return l.fset }

// ModulePath returns the module path declared in go.mod.
func (l *Loader) ModulePath() string { return l.modulePath }

func readModulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "example.com/root"
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], "\"")
		}
	}
	return "example.com/root"
}

// importPathFor maps a directory relative path to a Go import path.
func (l *Loader) importPathFor(relDir string) string {
	if relDir == "." {
		return l.modulePath
	}
	return l.modulePath + "/" + filepath.ToSlash(relDir)
}

// relPathFor maps an absolute path inside the module to a slash relative path.
func (l *Loader) relPathFor(abs string) string {
	rel, err := filepath.Rel(l.root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// IsLocalImport reports whether an import path belongs to this module.
func (l *Loader) IsLocalImport(path string) bool {
	return path == l.modulePath || strings.HasPrefix(path, l.modulePath+"/")
}

// Discover walks the root and builds package metadata. It is safe to call
// again after files on disk change; parsed-file caches for changed or removed
// files are dropped.
func (l *Loader) Discover() error {
	newMetas := map[string]*Meta{}
	newPkgFiles := map[string]string{}
	seen := map[string]bool{}

	err := filepath.WalkDir(l.root, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if abs != l.root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if skipFile(d.Name()) {
			return nil
		}
		dir := filepath.Dir(abs)
		relDir := l.relPathFor(dir)
		ip := l.importPathFor(relDir)
		seen[abs] = true
		newPkgFiles[abs] = ip
		m := newMetas[ip]
		if m == nil {
			m = &Meta{ImportPath: ip, Dir: dir, RelDir: relDir}
			newMetas[ip] = m
		}
		hash, herr := HashFile(abs)
		if herr != nil {
			return herr
		}
		m.Files = append(m.Files, &File{
			AbsPath: abs,
			RelPath: l.relPathFor(abs),
			Hash:    hash,
		})
		return nil
	})
	if err != nil {
		return err
	}

	// Parse files (cache hit when hash unchanged), collect package name and
	// module-local imports.
	for _, m := range newMetas {
		sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].RelPath < m.Files[j].RelPath })
		depSet := map[string]bool{}
		for _, f := range m.Files {
			parsed, perr := l.parseFile(f)
			if perr != nil {
				return perr
			}
			f.Syntax = parsed.Syntax
			if m.Name == "" {
				m.Name = parsed.Syntax.Name.Name
			}
			for _, imp := range parsed.Syntax.Imports {
				path := strings.Trim(imp.Path.Value, "\"")
				if l.IsLocalImport(path) {
					depSet[path] = true
				}
			}
		}
		m.LocalDeps = sortedKeys(depSet)
	}

	// Drop parse cache entries for files that disappeared.
	for abs := range l.parsed {
		if !seen[abs] {
			delete(l.parsed, abs)
		}
	}
	l.metas = newMetas
	l.pkgFiles = newPkgFiles
	return nil
}

// parsedCacheEntry caches a syntax tree by content hash.
type parsedCacheEntry struct {
	hash   string
	syntax *ast.File
}

func (l *Loader) parseFile(f *File) (*File, error) {
	if c, ok := l.parsed[f.AbsPath]; ok && c.Hash == f.Hash && c.Syntax != nil {
		return c, nil
	}
	src, err := os.ReadFile(f.AbsPath)
	if err != nil {
		return nil, err
	}
	sf, err := parser.ParseFile(l.fset, f.AbsPath, src, parser.ParseComments|parser.AllErrors)
	if err != nil {
		// Keep parsing errors visible but do not abort the whole scan: other
		// packages are still analyzable.
		if sf == nil {
			return nil, &ParseError{File: f.AbsPath, Err: err}
		}
	}
	entry := &File{AbsPath: f.AbsPath, RelPath: f.RelPath, Hash: f.Hash, Syntax: sf}
	l.parsed[f.AbsPath] = entry
	return entry, nil
}

// ParseError wraps a fatal parse error for a single file.
type ParseError struct {
	File string
	Err  error
}

func (e *ParseError) Error() string { return "parse " + e.File + ": " + e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// Meta returns metadata for a package path.
func (l *Loader) Meta(path string) *Meta { return l.metas[path] }

// Metas returns all discovered package metas.
func (l *Loader) Metas() map[string]*Meta { return l.metas }

// PackageOfFile returns the import path owning absPath, or "".
func (l *Loader) PackageOfFile(absPath string) string {
	if ip, ok := l.pkgFiles[absPath]; ok {
		return ip
	}
	if !filepath.IsAbs(absPath) {
		if ip, ok := l.pkgFiles[filepath.Join(l.root, filepath.FromSlash(absPath))]; ok {
			return ip
		}
	}
	return ""
}

// Checked returns an already type-checked package.
func (l *Loader) Checked(path string) *Package { return l.checked[path] }

// Packages returns all type-checked packages.
func (l *Loader) Packages() map[string]*Package { return l.checked }

// Dependencies returns module-local deps of a discovered package.
func (l *Loader) Dependencies(path string) []string {
	if m := l.metas[path]; m != nil {
		return m.LocalDeps
	}
	return nil
}

// order returns packages in topological (dependency-first) order.
func (l *Loader) order() ([]string, error) {
	tmp := map[string]bool{}
	done := map[string]bool{}
	var order []string
	var visit func(path string) error
	visit = func(path string) error {
		switch {
		case done[path]:
			return nil
		case tmp[path]:
			return &CycleError{Path: path}
		}
		tmp[path] = true
		for _, dep := range l.metas[path].LocalDeps {
			if _, ok := l.metas[dep]; !ok {
				continue // missing local package; skip
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		tmp[path] = false
		done[path] = true
		order = append(order, path)
		return nil
	}
	for _, ip := range sortedKeys(l.metas) {
		if err := visit(ip); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// CycleError reports an import cycle among module packages.
type CycleError struct{ Path string }

func (e *CycleError) Error() string { return "import cycle detected at " + e.Path }

// localImporter resolves module packages from cache and everything else via
// the gc export-data importer.
type localImporter struct {
	local    map[string]*types.Package
	fallback types.Importer
}

func (li *localImporter) Import(path string) (*types.Package, error) {
	if p, ok := li.local[path]; ok && p.Complete() {
		return p, nil
	}
	p, err := li.fallback.Import(path)
	if err != nil {
		// Return an empty but complete fake package so type checking of the
		// module can proceed; selectors into it may still fail individually.
		fake := types.NewPackage(path, pathLeaf(path))
		fake.Complete()
		return fake, nil
	}
	return p, nil
}

func pathLeaf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// checkPackage type-checks one package; its local deps must be checked first.
func (l *Loader) checkPackage(m *Meta) (*Package, error) {
	if p, ok := l.checked[m.ImportPath]; ok {
		return p, nil
	}
	local := map[string]*types.Package{}
	for ip, p := range l.checked {
		local[ip] = p.Types
	}
	conf := types.Config{
		Importer: &localImporter{local: local, fallback: l.fallback},
		Error:    func(err error) { /* collect best-effort */ },
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	syntax := make([]*ast.File, len(m.Files))
	for i, f := range m.Files {
		syntax[i] = f.Syntax
	}
	tp, err := conf.Check(m.ImportPath, l.fset, syntax, info)
	pkg := &Package{Meta: m, Types: tp, Info: info}
	m.CheckError = err
	l.checked[m.ImportPath] = pkg
	return pkg, err
}

// LoadAll discovers and type-checks every package under the root.
func (l *Loader) LoadAll() error {
	if err := l.Discover(); err != nil {
		return err
	}
	order, err := l.order()
	if err != nil {
		return err
	}
	var firstErr error
	for _, ip := range order {
		if _, err := l.checkPackage(l.metas[ip]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// EnsureChecked type-checks the named packages and their local dependencies.
// Already-checked packages are reused.
func (l *Loader) EnsureChecked(paths []string) error {
	order, err := l.order()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	var collect func(string)
	collect = func(p string) {
		if want[p] {
			return
		}
		want[p] = true
		if m := l.metas[p]; m != nil {
			for _, d := range m.LocalDeps {
				collect(d)
			}
		}
	}
	for _, p := range paths {
		collect(p)
	}
	var firstErr error
	for _, ip := range order {
		if !want[ip] {
			continue
		}
		if _, err := l.checkPackage(l.metas[ip]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Invalidate drops parse/check caches for the packages owning the given files
// and returns the set of affected import paths (owners only; reverse deps are
// computed by the incremental driver).
func (l *Loader) Invalidate(absFiles []string) map[string]bool {
	affected := map[string]bool{}
	for _, abs := range absFiles {
		if ip := l.PackageOfFile(abs); ip != "" {
			affected[ip] = true
		}
		delete(l.parsed, abs)
	}
	for ip := range affected {
		delete(l.checked, ip)
	}
	return affected
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
