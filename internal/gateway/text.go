package gateway

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxInboundBytes 是入站文本净化后保留的最大字节数（8 KiB）。
const MaxInboundBytes = 8 * 1024

// DefaultMaxChunk 是出站回复分片的默认上限（字符数）；略低于 Telegram 的 4096 以留出余量。
const DefaultMaxChunk = 4000

// SanitizeText 净化入站文本：丢弃非法 UTF-8、去掉除换行与制表符之外的控制字符与双向文本控制符
// （它们可用于伪装内容），把 CRLF 归一为 LF，按字节上限在字符边界截断，最后去掉首尾空白。
// maxBytes 不为正时使用 MaxInboundBytes。
func SanitizeText(s string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = MaxInboundBytes
	}
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	b.Grow(min(len(s), maxBytes))
	for _, r := range s {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || isBidiControl(r)) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// isBidiControl 识别双向文本的覆盖与隔离控制符（U+202A–U+202E、U+2066–U+2069）。
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

const codeFence = "```"

// SplitText 把长文本拆成每段不超过 maxRunes 个字符的若干段，供平台单条消息上限使用。
//
// 切分优先落在段落、换行、空格边界（只在后半段里找，避免产生过短的碎片）；
// 若切点落在 ``` 代码块内部，会在本段末尾补一个闭合围栏、下一段开头补一个开启围栏，
// 让每一段在平台上都能正确渲染。maxRunes 不为正时使用 DefaultMaxChunk；空文本返回 nil。
func SplitText(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxChunk
	}
	if s == "" {
		return nil
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return []string{s}
	}

	hasFence := strings.Contains(s, codeFence)
	fenceOpen := codeFence + "\n"
	fenceClose := "\n" + codeFence
	var out []string
	inFence := false
	for pos := 0; pos < len(runes); {
		limit := maxRunes
		if hasFence {
			// 为可能补上的开启/闭合围栏预留空间。
			limit -= utf8.RuneCountInString(fenceClose)
			if inFence {
				limit -= utf8.RuneCountInString(fenceOpen)
			}
		}
		if limit < 1 {
			limit = 1
		}

		var chunk []rune
		if remaining := len(runes) - pos; remaining <= limit {
			chunk = runes[pos:]
			pos = len(runes)
		} else {
			cut := findBreak(runes[pos : pos+limit])
			chunk = runes[pos : pos+cut]
			pos += cut
		}

		text := strings.Trim(string(chunk), "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		endsInFence := inFence
		if hasFence && countFenceLines(text)%2 == 1 {
			endsInFence = !inFence
		}
		if inFence {
			text = fenceOpen + text
		}
		if endsInFence && pos < len(runes) {
			text += fenceClose
		}
		inFence = endsInFence
		out = append(out, text)
	}
	return out
}

// findBreak 在窗口内寻找切点（返回切点之后的下标）：优先空行，其次换行，再次空格；
// 只接受落在窗口后半段的边界，找不到时整窗硬切。
func findBreak(window []rune) int {
	n := len(window)
	half := n / 2
	for i := n - 2; i >= half; i-- {
		if window[i] == '\n' && window[i+1] == '\n' {
			return i + 2
		}
	}
	for i := n - 1; i >= half; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := n - 1; i >= half; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	return n
}

// countFenceLines 统计以 ``` 开头的行数，用于判断切点是否落在代码块内。
func countFenceLines(text string) int {
	count := 0
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), codeFence) {
			count++
		}
	}
	return count
}

// replaceTokens 用 values 替换文案模板里的 {name} 占位符。
func replaceTokens(tmpl string, values map[string]string) string {
	for k, v := range values {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", v)
	}
	return tmpl
}
