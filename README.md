# nolsa —— Go 空指针 / 并发缺陷静态分析器

在**不执行代码**的前提下，对 Go 源文件做跨文件、跨函数的静态分析，发现：

- **空指针解引用** `nil-deref`（确定 / 可能 / 已被保护排除 三态）
- **同步原语被复制** `mutex-copy`
- **持锁路径未解锁 / 重复加解锁** `mutex-not-unlocked`
- **向空通道发送永久阻塞** `nil-channel-send`

特性：流敏感 + 过程间摘要传播、error/ok 失败模式精化、可定位调用路径、
行内注释与类别级压制、函数级**增量重分析**（增量结果与冷启动一致）、
按文件/行/列的确定性输出。

## 快速开始

```bash
go build ./...

# 分析文件（text）
go run ./cmd/nolsa path/to/a.go path/to/b.go

# JSON、关闭某类别、审计被压制项
go run ./cmd/nolsa -json -disable mutex-copy -show-suppressed a.go
```

## 库用法

```go
import (
    "github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
    "github.com/guoyyit/nil-concurrency-analyzer-go/internal/service"
)

svc := service.New()
diags, _ := svc.Analyze([]loader.InputFile{
    {Path: "a.go", Content: src},
}, service.DefaultOptions())

// 传入变更后的完整文件集合再次 Analyze 即增量重分析
diags2, _ := svc.Analyze(updated, service.DefaultOptions())
```

## 行内压制

```go
// nolsa:ignore nil-deref
return *p
```

## 测试

```bash
go test ./...
```

## 文档

- [docs/design.md](docs/design.md)：精度模型、增量算法、配置项、已知局限。
