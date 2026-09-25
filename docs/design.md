# nolsa —— Go 空指针与并发缺陷静态分析器

`nolsa` 在**不执行代码**的前提下，对一组 Go 源文件做静态分析，发现两类缺陷：

1. 可能在运行时触发 panic 的**空指针解引用**（`nil-deref`）；
2. 若干常见的**并发使用缺陷**（`mutex-copy`、`mutex-not-unlocked`、`nil-channel-send`）。

分析覆盖**跨文件、跨函数**的调用关系，诊断可定位（文件/行/列 + 调用路径）、
可复现（确定性全序输出）、可压制（行内注释 + 类别全局开关），并支持**增量重分析**。

---

## 1. 架构总览

```
loader（解析 + go/types 类型信息）
   │  每轮在全新 token.FileSet 中重解析全部文件；用内容哈希检测变化文件
   ▼
nilflow（空值分析）
   ├── 过程内：流敏感的空值格传播（CFG 风格的分支/合并）
   ├── 过程间：函数摘要（返回值空值、解引用形参）+ 固定点迭代
   └── nil 实参沿调用边做上下文敏感内联（带递归守卫），产出调用路径
   ▼
concurrency（并发检查）
   ├── mutex-copy：同步原语按值复制（结构体字段/数组元素穿透）
   ├── mutex-not-unlocked：路径敏感的 Lock/Unlock 配对
   └── nil-channel-send：复用 nilflow 的通道空值程序点
   ▼
suppress（行内注释 + 类别开关）→ diag（全序排序 + 去重）→ 输出
```

包划分：

| 包 | 职责 |
|----|------|
| `internal/loader` | 源文件解析、类型检查、按内容哈希的变化检测 |
| `internal/nilflow` | 空值格、过程内/过程间分析、函数摘要、增量闭包哈希 |
| `internal/concurrency` | 三类并发缺陷检查 |
| `internal/suppress` | 行内压制注释与全局类别开关 |
| `internal/diag` | 诊断类型、确定性排序与去重 |
| `internal/service` | 组装编排、增量作用域、压制应用、统计 |
| `cmd/nolsa` | 命令行入口 |

---

## 2. 空指针分析精度模型

### 2.1 空值格（lattice）

每个指针类表达式（指针、interface、slice/map/chan/func 等引用类型）
被抽象为四值格：

| 值 | 含义 | 解引用结论 |
|----|------|-----------|
| `nil` | 在该程序点确定为 nil（零值、`nil` 字面量、返回 nil 的调用） | **确定崩溃**（high / definite） |
| `non-nil` | 确定非 nil（`new`、`&x`、判空保护分支、`make`） | 安全（可输出为 excluded 证据） |
| `maybe-nil` | 控制流合并后可能为 nil | **可疑**（suspicious / possible） |
| `unknown` | 无信息（外部函数、参数初值、map 索引结果等） | **不报告**（保守，避免误报） |

`join(nil, non-nil) = maybe-nil`；`unknown` 是格底，与任何值合并取另一值。

设计取向是**宁可漏报、不可滥报**：对完全无信息的值保持沉默，只有
“确定”或“在已知路径上可能”才产生诊断。

### 2.2 过程内分析（流敏感）

- 按语句顺序解释，对 `if/else`、`for`、`switch`、`type switch`、`select`
  做分支分裂与结束点合并；
- 支持 `:=` / `=` / 多返回值赋值、`var` 声明零值、`return` 合并；
- 解引用点覆盖：`*p`、`p.field`（指针字段选择）、`(*[N]T)[i]`、
  左值写入 `*p = …`、`p.f = …`；
- map/slice 索引默认**不**报 nil 解引用（map 读 nil 返回零值不崩溃；
  map 写 nil 属另一类 panic，当前未建模）。

### 2.3 跨函数传播

**函数摘要（Summary）** 记录：

- `Results[i]`：每个返回值的空值抽象（跨返回语句合并）；
- `DerefParams`：函数体内**无保护解引用**的形参下标；
- `ResultPos / DerefPos`：代表性位置，用于调用路径展示。

包级分析以固定点迭代计算所有函数摘要（按稳定顺序迭代直至不变）。

**失败模式（error / bool ok）**：在调用点为每个结果打上 `CallRef`
（来自哪个调用的第几个返回值），在条件分支上做“兄弟返回值精化”：

- `p, err := f(); if err == nil { … }`：成功分支把同一次调用的 `p`
  精化为 non-nil；失败分支 `err != nil` 上 `p` 可能为 nil；
- `v, ok := x.(T)` / `<-ch`：`ok == true` 蕴含 `v` 非 nil；
- map 二值结果不蕴含非 nil（map 命中与否与空值无关）。

**调用路径与内联**：当实参在调用点确定为 nil 时，分析器以该实参特化被调
函数形参，沿调用边做一次上下文敏感的“内联”（受活动函数集合剪枝，递归安全），
从而：

- 在真正解引用点（可能跨多层函数）产出确定诊断；
- 调用路径形如 `caller:line → callee:line → leaf:deref`；
- **调用方判空保护会随实参 non-nil 传播**：仅在调用方保护下传入的解引用
  不会被误报（形参在被调内联中标记为“受保护”）。

### 2.4 结论三态

每条 nil 诊断带 `certainty`：

- `definite`（配合 `confidence=high`）：确定为 nil 的解引用；
- `possible`（配合 `confidence=suspicious`）：路径合并后可能为 nil；
- `excluded`：被保护分支证明非 nil —— 默认不输出，加 `-show-excluded`
  可作为“已检查且安全”的证据查看。

### 2.5 误报控制要点

- 未知（unknown）解引用默认沉默；
- 调用方已判空的形参沿调用链保持受保护；
- 同一真实缺陷只在**实际解引用点**报告一次：同包函数用内联统一报告，
  `DerefParams` 摘要仅对无函数体/外部函数作为回退；
- 诊断最终按全序排序并去重。

---

## 3. 并发缺陷

### 3.1 `mutex-copy`（同步原语被复制）

识别以下标准库类型（含在结构体字段、数组元素中的嵌套嵌入）：
`sync.Mutex`、`sync.RWMutex`、`sync.WaitGroup`、`sync.Cond`、`sync.Once`、
`sync/atomic.*`、`errgroup.Group`。

报告点：

- **high**：调用点按值传参、按值接收者（方法定义点）、range/send/赋值/
  解引用实参造成的复制；
- **suspicious**：函数按值返回含锁类型（在结果签名处统一报一次，
  不对每个 return 重复报）。

指针/切片传递不算复制。

### 3.2 `mutex-not-unlocked`（路径上未解锁等）

在单函数内做路径敏感的锁状态机：`unlocked / locked / deferred-unlock / maybe`。

- **high**：某条 return 路径或函数自然结束时仍持有锁；对已持锁再次
  `Lock`（自锁死）；
- **suspicious**：未持锁即 `Unlock`；可能的重复解锁。

`defer mu.Unlock()`（无论出现在 Lock 前后）被正确识别为“退出时必然解锁”。

### 3.3 `nil-channel-send`（向空通道发送永久阻塞）

复用 nilflow 对通道表达式的空值分析：

- 确定 nil（未初始化、被赋 nil）→ **high / definite**；
- 分支合并后可能 nil → **suspicious / possible**；
- `make(chan …)` 等非 nil → 不报。

接收 `<-ch` 从 nil 通道是“永久阻塞但不 panic”，当前不纳入该类别。

---

## 4. 增量重分析

### 4.1 变化检测与受影响范围

- `loader` 用内容 SHA1 检测变化文件；**每轮都在全新 `token.FileSet` 中
  重新解析并类型检查全部文件**，保证所有 `token.Pos` 相对本轮一致
  （跨轮复用 AST/Pos 会让缓存位置失效）。
- 函数体用 `go/printer` 做**位置无关哈希**；
- 为每个函数计算**依赖闭包哈希**
  `H(f) = hash(body(f), sorted(H(callee) for callee in calls(f))`，
  迭代到不动点。函数自身或其任意（传递）被调函数变化都会改变 `H(f)`。
- `DirtyFunctions` = 闭包哈希相对缓存发生变化的函数集合。该集合天然就是
  “受影响范围”，无需再显式做调用图反向传播。

### 4.2 一致性保证

- 脏函数重新做固定点分析并覆盖缓存；非脏函数原样重放其摘要与诊断；
- **跨轮缓存与 `token.Pos` 解耦**：每轮在全新 FileSet 上解析，函数诊断
  以 FileSet 无关的 `loc`（文件 + 函数内相对行偏移 + 列）缓存，重放时
  用新 AST 中同名函数的声明行重新定位，因此代码整体平移行号后诊断仍精确；
- 并发检查（copylock、mutex 路径）与 `nil-channel-send` 程序点状态只
  依赖轻量的包级/函数内遍历，每轮对全部函数重算（不缓存 `token.Pos`），
  既保证位置正确，开销也很小；空值固定点才是函数级缓存的重点；
- 输出统一排序去重。

因此**增量结果与冷启动全量结果逐项一致**（由
`TestIncrementalEqualsCold` 在多步编辑/回退序列上验证）。

`Service.Stats()` 返回本轮重算的函数数 `Recomputed` 与是否完全命中
`HitCache`。

---

## 5. 压制与配置

### 5.1 行内注释

```go
// nolsa:ignore               // 压制下一行（或本行尾部）的全部类别
// nolsa:ignore nil-deref     // 仅压制 nil-deref
// nolsa:ignore nil-deref,nil-channel-send
x := *p //nolsa:ignore       // 行尾形式同样支持
```

另兼容 `nolint:ignore` 前缀。

作用域规则（保证不误伤）：

- 只作用于注释所在行或其**下一行**；
- 指定类别时只压制该类别，其它类别照常报告；
- 只作用于所在文件；
- 全局关闭的类别同样被视为“被压制”。

加 `-show-suppressed` 可保留被压制诊断并置 `suppressed=true`（审计用）。

### 5.2 全局类别开关

CLI：`-disable nil-deref,mutex-copy`。
库 API：`service.Options{ Suppress: suppress.Config{ Enabled: map[...]bool{...} } }`，
某类别显式置 `false` 即全局关闭；缺省/`nil` 表示全部启用。

### 5.3 类别与置信度标识

| Category | 含义 |
|----------|------|
| `nil-deref` | 空指针解引用 |
| `mutex-copy` | 同步原语按值复制 |
| `mutex-not-unlocked` | 持锁路径未解锁 / 重复加解锁 |
| `nil-channel-send` | 向空通道发送 |

`confidence ∈ {high, suspicious}`；nil 相关另有
`certainty ∈ {definite, possible, excluded}`。

---

## 6. 用法

```bash
# 分析若干文件（text 输出；有诊断时退出码为 1）
nolsa ./.../a.go ./.../b.go

# JSON 输出
nolsa -json a.go

# 全局关闭某类别 / 查看被排除证据 / 审计被压制项
nolsa -disable mutex-copy -show-excluded -show-suppressed a.go

# 从 stdin 读取文件集合（JSON: [{"path","content"}]）
nolsa -stdin < files.json
```

text 输出形如：

```
path/a.go:12:5: [nil-deref/high] nil passed to ... which dereferences parameter x ...
    via pkg.caller (path/b.go:30:9)
    via pkg.leaf (path/a.go:12:5)
```

库用法：

```go
svc := service.New()
diags, _ := svc.Analyze([]loader.InputFile{{Path: "a.go", Content: src}},
    service.DefaultOptions())
// 同一 svc 再次 Analyze 变更后的完整文件集即为增量重分析
```

---

## 7. 已知局限

1. **跨包过程间分析**：仅对同包函数建立摘要与内联；对外部包函数调用
   保守视为 unknown（不据此报确定缺陷，也不传播其返回 nil）。标准库的
   常见构造（`new/make/&x/nil`）已特判。
2. **闭包 / goroutine / defer 体**：`func(){…}()` 闭包体不在当前路径内
   顺序执行，当前不进入其体做空值路径分析；`defer`/`go` 的实参仍求值。
3. **指针别名与堆逃逸**：过程内以标识符/对象维度跟踪，对复杂别名
   （`*p` 间接写、interface 装箱后再拆）建模有限，倾向保守沉默。
4. **map 写 nil、slice 越界、nil receiver 方法调用** 等其它运行时 panic
   不在范围内。
5. **反射、unsafe、cgo** 不做建模。
6. 循环不展开，采用一次“入口∪回边”的保守合并，可能把确定值降为 maybe。
7. 条件精化聚焦 `x == nil` / `err != nil` / bool `ok` 等常见形式；
   更复杂的守卫（如 `errors.Is`、自定义 `if valid(p)`）不识别。
8. 泛型实例化按泛型函数实体分析；实例化类型差异不特化空值。

这些局限总体导向**低误报**：拿不准时不报 high，必要时降级为 suspicious。
