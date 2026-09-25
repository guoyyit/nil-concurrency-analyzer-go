package analysis

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// checkNilChannels detects sends to, and closing of, channels that are nil on
// the reaching path. A send on a nil channel blocks forever; close(nil)
// panics.
func (p *Program) checkNilChannels() {
	if !p.cfg.IsEnabled(config.NilChanSend) {
		return
	}
	for _, fd := range p.funcs {
		if fd.decl.Body == nil {
			continue
		}
		r := p.runWithRunner(fd, nil)
		w := &nilChanWalker{r: r, fd: fd, pg: p}
		e := p.newEnv(fd)
		for _, v := range fd.params {
			if st, ok := r.paramEntry[v]; ok {
				e.set(v, st)
			}
		}
		w.block(fd.decl.Body.List, e)
		fd.diags = append(fd.diags, w.diags...)
	}
}

type nilChanWalker struct {
	r     *runner
	fd    *funcDecl
	pg    *Program
	diags []*Diagnostic
}

func (w *nilChanWalker) block(list []ast.Stmt, e *env) {
	for _, st := range list {
		if e == nil {
			return
		}
		e = w.stmt(st, e)
	}
}

func (w *nilChanWalker) stmt(st ast.Stmt, e *env) *env {
	switch s := st.(type) {
	case *ast.BlockStmt:
		w.block(s.List, e.fork())
		return e

	case *ast.AssignStmt:
		// reuse nil analysis binding by running a fresh mini transfer:
		// simplest correct approach is to mirror state updates for channels.
		return w.assign(s, e)

	case *ast.DeclStmt:
		return w.decl(s, e)

	case *ast.SendStmt:
		st := e.eval(s.Chan).state
		if st == stateNil {
			w.report(s.Chan.Pos(), config.Certain, "send on a channel that is nil on this path blocks forever")
		} else if st == stateMayNil {
			w.report(s.Chan.Pos(), config.Possible, "send on a channel that may be nil on this path")
		}
		r := w.pg.newRunner(w.fd)
		r.checkExpr(s.Chan, e)
		r.checkExpr(s.Value, e)
		return e

	case *ast.ExprStmt:
		w.checkClose(s.X, e)
		return e

	case *ast.IfStmt:
		if s.Init != nil {
			e = w.stmt(s.Init, e)
		}
		thenE := w.r.condEnvs(s.Cond, e, true)
		elseE := w.r.condEnvs(s.Cond, e, false)
		w.block(s.Body.List, thenE)
		if s.Else != nil {
			w.stmt(s.Else, elseE)
		}
		return mergeEnvs([]*env{thenE, elseE})

	case *ast.ForStmt:
		body := e.fork()
		if s.Cond != nil {
			w.r.refine(s.Cond, true, body)
		}
		w.block(s.Body.List, body)
		exit := e.fork()
		if s.Cond != nil {
			w.r.refine(s.Cond, false, exit)
		}
		return exit

	case *ast.RangeStmt:
		w.block(s.Body.List, e.fork())
		return e

	case *ast.SwitchStmt:
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			w.block(clause.Body, e.fork())
		}
		return e
	case *ast.TypeSwitchStmt:
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			w.block(clause.Body, e.fork())
		}
		return e
	case *ast.SelectStmt:
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CommClause)
			br := e.fork()
			if send, ok := clause.Comm.(*ast.SendStmt); ok {
				st := br.eval(send.Chan).state
				if st == stateNil {
					w.report(send.Chan.Pos(), config.Certain, "send on a nil channel inside select blocks this case forever")
				}
			}
			w.block(clause.Body, br)
		}
		return e

	case *ast.GoStmt:
		return e
	case *ast.DeferStmt:
		return e
	case *ast.ReturnStmt, *ast.BranchStmt:
		return nil
	}
	return e
}

func (w *nilChanWalker) checkClose(x ast.Expr, e *env) {
	call, ok := x.(*ast.CallExpr)
	if !ok {
		return
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "close" || len(call.Args) != 1 {
		return
	}
	if _, isChan := underlying(w.fd.pkg.Info.TypeOf(call.Args[0])).(*types.Chan); !isChan {
		return
	}
	st := e.eval(call.Args[0]).state
	switch st {
	case stateNil:
		w.report(call.Args[0].Pos(), config.Certain, "close of a channel that is nil on this path panics")
	case stateMayNil:
		w.report(call.Args[0].Pos(), config.Possible, "close of a channel that may be nil on this path")
	}
}

func (w *nilChanWalker) report(pos token.Pos, conf config.Confidence, msg string) {
	w.diags = append(w.diags, &Diagnostic{
		Category:   config.NilChanSend,
		Confidence: conf,
		Pos:        loader.PosOf(w.pg.fset, pos),
		Message:    msg,
	})
}

// assign mirrors nil-analysis binding for channel-typed variables so channel
// states stay accurate while walking.
func (w *nilChanWalker) assign(s *ast.AssignStmt, e *env) *env {
	r := w.pg.newRunner(w.fd)
	r.varRels = map[*types.Var][]*rel{}
	return r.assign(s, e)
}

func (w *nilChanWalker) decl(s *ast.DeclStmt, e *env) *env {
	r := w.pg.newRunner(w.fd)
	r.varRels = map[*types.Var][]*rel{}
	return r.decl(s, e)
}
