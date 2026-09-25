// Command nolsa 是 nil/concurrency 静态分析服务的命令行入口。
//
// 用法：
//
//	nolsa [flags] file1.go file2.go ...
//
// 标志：
//
//	-json              以 JSON 输出诊断
//	-disable cats      逗号分隔的缺陷类别全局关闭
//	-show-excluded     输出被保护分支排除的证据
//	-show-suppressed   保留被压制的诊断（标记 suppressed）
//	-stdin             从标准机读取一组文件（JSON: [{path,content}]）
//
// 同一文件集合重复调用（经 stdin 长驻模式外的多次运行）时，可通过
// -server 启用增量重分析；普通一次性运行做冷启动全量分析。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/service"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

func main() {
	var (
		asJSON         = flag.Bool("json", false, "emit JSON diagnostics")
		disable        = flag.String("disable", "", "comma-separated categories to disable")
		showExcluded   = flag.Bool("show-excluded", false, "show guarded/excluded evidence")
		showSuppressed = flag.Bool("show-suppressed", false, "keep suppressed diagnostics (marked)")
		fromStdin      = flag.Bool("stdin", false, "read files as JSON array [{path,content}] from stdin")
	)
	flag.Parse()

	opts := service.DefaultOptions()
	opts.ShowExcluded = *showExcluded
	opts.ShowSuppressed = *showSuppressed
	cfg := suppress.DefaultConfig()
	for _, c := range strings.Split(*disable, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			cfg.Enabled[diag.Category(c)] = false
		}
	}
	opts.Suppress = cfg

	svc := service.New()

	var (
		diags []diag.Diagnostic
		err   error
	)
	if *fromStdin {
		var files []loader.InputFile
		data, _ := io.ReadAll(os.Stdin)
		if err := json.Unmarshal(data, &files); err != nil {
			fatal("invalid stdin JSON: " + err.Error())
		}
		diags, err = svc.Analyze(files, opts)
	} else {
		diags, err = svc.AnalyzePaths(flag.Args(), opts)
	}
	if err != nil {
		fatal(err.Error())
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(diags); err != nil {
			fatal(err.Error())
		}
	} else {
		for _, d := range diags {
			fmt.Printf("%s:%d:%d: [%s/%s] %s\n",
				d.Position.File, d.Position.Line, d.Position.Column,
				d.Category, d.Confidence, d.Message)
			for _, n := range d.CallPath {
				fmt.Printf("    via %s (%s:%d)\n", n.Func, n.Position.File, n.Position.Line)
			}
		}
	}
	realDefects := 0
	for _, d := range diags {
		if d.Certainty == diag.CertaintyExcluded || d.Suppressed {
			continue
		}
		realDefects++
	}
	if realDefects > 0 {
		os.Exit(1)
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "nolsa: "+msg)
	os.Exit(2)
}
