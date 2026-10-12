package main

import (
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxCellWidth 是表格单元格的最大显示宽度；更长的内容截断并加省略号，避免一行撑爆终端。
const maxCellWidth = 48

// table 是按列对齐的纯文本表格。宽度按终端显示宽度计算（中日韩全角字符占 2 列），
// 而不是按字节或码点，否则中文表头与内容会错位。
type table struct {
	header []string
	rows   [][]string
}

func newTable(header ...string) *table { return &table{header: header} }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

// render 写出表格：列间两个空格，末列不补空格。
func (t *table) render(w io.Writer) error {
	widths := make([]int, len(t.header))
	clip := func(s string) string { return truncateWidth(oneLine(s), maxCellWidth) }
	measure := func(row []string) {
		for i := range widths {
			if i < len(row) {
				if n := displayWidth(clip(row[i])); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	measure(t.header)
	for _, r := range t.rows {
		measure(r)
	}
	var b strings.Builder
	writeRow := func(row []string) {
		for i := range widths {
			cell := ""
			if i < len(row) {
				cell = clip(row[i])
			}
			b.WriteString(cell)
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	writeRow(t.header)
	for _, r := range t.rows {
		writeRow(r)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// displayWidth 返回字符串在等宽终端中的显示宽度。
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// runeWidth 是单个字符的显示宽度：组合字符与零宽字符为 0，东亚宽字符与全角字符为 2，其余为 1。
func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == '​' || r == '‍':
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// isWide 判断字符是否占两列（覆盖常见的中日韩统一表意文字、假名、谚文、全角符号与表情）。
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 谚文字母
		r >= 0x2E80 && r <= 0x303E,   // 中日韩部首、符号与标点
		r >= 0x3041 && r <= 0x33FF,   // 假名、注音、中日韩兼容
		r >= 0x3400 && r <= 0x4DBF,   // 扩展 A
		r >= 0x4E00 && r <= 0x9FFF,   // 统一表意文字
		r >= 0xA000 && r <= 0xA4CF,   // 彝文
		r >= 0xAC00 && r <= 0xD7A3,   // 谚文音节
		r >= 0xF900 && r <= 0xFAFF,   // 兼容表意文字
		r >= 0xFE30 && r <= 0xFE4F,   // 兼容形式
		r >= 0xFF00 && r <= 0xFF60,   // 全角 ASCII
		r >= 0xFFE0 && r <= 0xFFE6,   // 全角符号
		r >= 0x1F300 && r <= 0x1F64F, // 表情
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD: // 扩展 B 及以后
		return true
	}
	return false
}

// truncateWidth 按显示宽度截断，超出时以 "…" 结尾。
func truncateWidth(s string, max int) string {
	if displayWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteString("…")
	return b.String()
}

// oneLine 把多行文本压成一行（换行与制表替换为空格），供表格与一行摘要使用。
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n\t") {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}

// clipText 把文本压成一行并按字符数截断。
func clipText(s string, max int) string {
	s = oneLine(strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// writeJSON 以缩进 JSON 写出 v。
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// listJSON 是列表在 --json 模式下的形状，与 API 一致：{"items": [...]}。
type listJSON[T any] struct {
	Items []T `json:"items"`
}

// itemsOf 包装列表；nil 输出为 [] 而不是 null，方便脚本直接遍历。
func itemsOf[T any](list []T) listJSON[T] {
	if list == nil {
		list = []T{}
	}
	return listJSON[T]{Items: list}
}

// formatTime 把时间格式化为本地时区的分钟精度；零值为空。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// formatTimePtr 同 formatTime，接受指针。
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

// yesNo 把布尔值显示为 ✓ / 空。
func yesNo(b bool) string {
	if b {
		return "✓"
	}
	return "-"
}
