package tools

import (
	"html"
	"strings"
)

// 本文件把 HTML 简化为纯文本：去掉脚本与样式、把块级标签换成换行、解码实体、压缩空白。
// 只做"够模型阅读"的简化，不追求完整的 HTML 解析（不引入第三方依赖）。

// skipElements 是整段丢弃的元素（含内容）。
var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true, "iframe": true,
}

// blockElements 是前后换行的元素。
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "section": true, "article": true, "header": true, "footer": true,
	"ul": true, "ol": true, "table": true, "pre": true, "blockquote": true, "hr": true, "dd": true, "dt": true,
	"title": true, "nav": true, "aside": true, "main": true, "form": true, "option": true, "thead": true, "tbody": true,
}

// htmlToText 把 HTML 转成纯文本。
func htmlToText(s string) string {
	var b strings.Builder
	b.Grow(len(s) / 2)
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				j = len(s) - i
			}
			b.WriteString(html.UnescapeString(s[i : i+j]))
			i += j
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		tag := s[i+1 : i+end]
		i += end + 1
		closing := strings.HasPrefix(tag, "/")
		name := tagName(strings.TrimPrefix(tag, "/"))
		if name == "" {
			continue
		}
		if !closing && skipElements[name] {
			closeTag := "</" + name
			rest := strings.ToLower(s[i:])
			k := strings.Index(rest, closeTag)
			if k < 0 {
				break
			}
			i += k
			if gt := strings.IndexByte(s[i:], '>'); gt >= 0 {
				i += gt + 1
			} else {
				break
			}
			continue
		}
		if blockElements[name] {
			b.WriteString("\n")
		} else if name == "td" || name == "th" {
			b.WriteString(" ")
		}
	}
	return normalizeText(b.String())
}

// tagName 取标签的名称部分（小写），例如 "a href=..." → "a"，"br/" → "br"。
func tagName(tag string) string {
	tag = strings.TrimSpace(tag)
	end := 0
	for end < len(tag) {
		c := tag[end]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '/' || c == '>' {
			break
		}
		end++
	}
	return strings.ToLower(tag[:end])
}

// normalizeText 把每行内部的空白压成单个空格、去掉首尾空白，并丢弃空行：
// 给模型看的文本以紧凑为先，段落边界由换行表达即可。
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
