package diag

import "sort"

// Confidence 描述诊断结论的置信等级。
type Confidence string

const (
	ConfidenceHigh       Confidence = "high"       // 高置信：在对应路径上缺陷确定成立
	ConfidenceSuspicious Confidence = "suspicious" // 可疑：保守分析下可能成立
)

// Certainty 描述空指针结论的确定程度。
type Certainty string

const (
	CertaintyDefinite Certainty = "definite" // 确定为 nil
	CertaintyPossible Certainty = "possible" // 可能为 nil
	CertaintyExcluded Certainty = "excluded" // 被保护分支排除（仅证据模式输出）
)

// Category 是缺陷类别标识。
type Category string

const (
	CatNilDeref         Category = "nil-deref"          // 空指针解引用
	CatMutexCopy        Category = "mutex-copy"         // 同步原语被复制
	CatMutexNotUnlocked Category = "mutex-not-unlocked" // 持锁路径未解锁
	CatNilChanSend      Category = "nil-channel-send"   // 向空通道发送
)

// Position 是一个源码位置。
type Position struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// CallNode 是调用路径上的一个节点。
type CallNode struct {
	Func     string   `json:"func"`
	Position Position `json:"position"`
}

// Diagnostic 是一条可定位的诊断结果。
type Diagnostic struct {
	Category   Category   `json:"category"`
	Position   Position   `json:"position"`
	Message    string     `json:"message"`
	Confidence Confidence `json:"confidence"`
	Certainty  Certainty  `json:"certainty,omitempty"`
	CallPath   []CallNode `json:"callPath,omitempty"`
	Suppressed bool       `json:"suppressed,omitempty"`
}

// less 定义诊断之间的全序：文件、行、列、类别、置信度、结论、消息。
func less(a, b Diagnostic) bool {
	if a.Position.File != b.Position.File {
		return a.Position.File < b.Position.File
	}
	if a.Position.Line != b.Position.Line {
		return a.Position.Line < b.Position.Line
	}
	if a.Position.Column != b.Position.Column {
		return a.Position.Column < b.Position.Column
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	if a.Confidence != b.Confidence {
		return a.Confidence < b.Confidence
	}
	if a.Certainty != b.Certainty {
		return a.Certainty < b.Certainty
	}
	if a.Message != b.Message {
		return a.Message < b.Message
	}
	return callPathLess(a.CallPath, b.CallPath)
}

func callPathLess(a, b []CallNode) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) {
			return true
		}
		if i >= len(b) {
			return false
		}
		x, y := a[i], b[i]
		if x.Func != y.Func {
			return x.Func < y.Func
		}
		if x.Position.File != y.Position.File {
			return x.Position.File < y.Position.File
		}
		if x.Position.Line != y.Position.Line {
			return x.Position.Line < y.Position.Line
		}
		if x.Position.Column != y.Position.Column {
			return x.Position.Column < y.Position.Column
		}
	}
	return false
}

// Sort 按全序排序并就地去重，返回结果切片，保证相同输入产生完全一致的输出。
func Sort(diags []Diagnostic) []Diagnostic {
	sort.Slice(diags, func(i, j int) bool { return less(diags[i], diags[j]) })
	if len(diags) < 2 {
		return diags
	}
	out := diags[:1]
	for i := 1; i < len(diags); i++ {
		if !equal(diags[i], out[len(out)-1]) {
			out = append(out, diags[i])
		}
	}
	// 清空尾部避免悬挂引用。
	for i := len(out); i < len(diags); i++ {
		diags[i] = Diagnostic{}
	}
	return out
}

func equal(a, b Diagnostic) bool {
	return !less(a, b) && !less(b, a)
}
