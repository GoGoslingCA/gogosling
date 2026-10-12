package tools

import (
	"fmt"
	"unicode/utf8"
)

// DefaultMaxResultBytes 是回填给模型的工具结果默认上限（64 KiB）。
const DefaultMaxResultBytes = 64 << 10

// truncateUTF8 把 s 截到不超过 max 字节，并保证不在多字节字符中间切断。
func truncateUTF8(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// clipResult 把工具结果截到 max 字节，并在末尾追加说明；未超限时原样返回。
func clipResult(content string, max int) (string, bool) {
	out, truncated := truncateUTF8(content, max)
	if !truncated {
		return content, false
	}
	return out + fmt.Sprintf("\n\n[结果已截断：原始长度 %d 字节，仅保留前 %d 字节]", len(content), len(out)), true
}
