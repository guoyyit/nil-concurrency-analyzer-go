// Package suppress 实现基于注释的局部压制与按类别的全局开关。
//
// 行内压制语法（规范形式，另兼容 nolint 前缀）：
//
//	// nolsa:ignore                 // 压制下一行/本行的全部类别
//	// nolsa:ignore nil-deref       // 仅压制指定类别
//	// nolsa:ignore nil-deref,nil-channel-send
//
// 压制只作用于注释所在行或其下一行，且类别必须匹配，不会误伤其他位置或其他诊断。
package suppress

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// Config 是全局压制配置。
type Config struct {
	// Enabled 中显式置 false 的类别被全局关闭；nil 或缺省表示启用。
	Enabled map[diag.Category]bool
}

func DefaultConfig() Config { return Config{Enabled: map[diag.Category]bool{}} }

// CategoryEnabled 判断某类别是否全局启用。
func (c Config) CategoryEnabled(cat diag.Category) bool {
	if c.Enabled == nil {
		return true
	}
	v, ok := c.Enabled[cat]
	return !ok || v
}

// Suppression 表示一条行内压制指令。
type Suppression struct {
	File       string
	Line       int
	Categories map[diag.Category]bool
	All        bool
}

func (s Suppression) covers(cat diag.Category) bool {
	return s.All || s.Categories[cat]
}

// Engine 应用局部与全局压制规则。
type Engine struct {
	cfg   Config
	lines map[string]map[int][]Suppression // file -> line (注释所在行)
}

func NewEngine(cfg Config) *Engine {
	return &Engine{cfg: cfg, lines: map[string]map[int][]Suppression{}}
}

// ParseComments 从一组文件的注释中收集压制指令。
func (e *Engine) ParseComments(fset *token.FileSet, files []*ast.File) {
	e.lines = map[string]map[int][]Suppression{}
	for _, f := range files {
		if f == nil {
			continue
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				sup, ok := parseDirective(c.Text)
				if !ok {
					continue
				}
				pos := fset.Position(c.Pos())
				sup.File = pos.Filename
				sup.Line = pos.Line
				if e.lines[sup.File] == nil {
					e.lines[sup.File] = map[int][]Suppression{}
				}
				e.lines[sup.File][sup.Line] = append(e.lines[sup.File][sup.Line], sup)
			}
		}
	}
}

// parseDirective 识别 //nolsa:ignore ... 或 //nolint:ignore ... 注释。
func parseDirective(text string) (Suppression, bool) {
	body := text
	switch {
	case strings.HasPrefix(body, "//"):
		body = body[2:]
	case strings.HasPrefix(body, "/*"):
		body = strings.TrimSuffix(body[2:], "*/")
	}
	body = strings.TrimSpace(body)
	var rest string
	switch {
	case strings.HasPrefix(body, "nolsa:ignore"):
		rest = strings.TrimPrefix(body, "nolsa:ignore")
	case strings.HasPrefix(body, "nolint:ignore"):
		rest = strings.TrimPrefix(body, "nolint:ignore")
	case body == "nolsa:ignore" || body == "nolint:ignore":
		rest = ""
	default:
		return Suppression{}, false
	}
	sup := Suppression{All: true, Categories: map[diag.Category]bool{}}
	rest = strings.TrimSpace(rest)
	if rest != "" {
		sup.All = false
		for _, name := range strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			name = strings.TrimSpace(name)
			if name != "" {
				sup.Categories[diag.Category(name)] = true
			}
		}
	}
	return sup, true
}

// IsSuppressed 判断某文件某行的某类诊断是否被行内指令压制（不含全局开关）。
func (e *Engine) IsSuppressed(file string, line int, cat diag.Category) bool {
	byLine := e.lines[file]
	for _, ln := range []int{line, line - 1} {
		for _, sup := range byLine[ln] {
			if sup.covers(cat) {
				return true
			}
		}
	}
	return false
}

// Apply 就地标记被压制（行内或全局关闭）的诊断；被压制项从返回切片中移除，
// 但当 keepSuppressed 为真时保留并置 Suppressed=true（用于测试/审计）。
func (e *Engine) Apply(diags []diag.Diagnostic, keepSuppressed bool) []diag.Diagnostic {
	out := diags[:0]
	for _, d := range diags {
		suppressed := !e.cfg.CategoryEnabled(d.Category) ||
			e.IsSuppressed(d.Position.File, d.Position.Line, d.Category)
		if suppressed {
			d.Suppressed = true
			if keepSuppressed {
				out = append(out, d)
			}
			continue
		}
		out = append(out, d)
	}
	return out
}
