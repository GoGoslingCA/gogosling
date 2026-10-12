package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// defaultSearchResults / maxSearchResults 是 search_files 的默认与最大结果数。
	defaultSearchResults = 50
	maxSearchResults     = 200
	// maxSearchFileBytes 是参与内容搜索的单文件大小上限；更大的文件跳过。
	maxSearchFileBytes = 1 << 20
	// maxSearchVisits 是一次搜索最多访问的条目数（包含目录），防止巨大目录树或空目录拖长扫描。
	maxSearchVisits = 20000
	// maxSearchContextLines 限制每条匹配附带的前后上下文，控制结果大小。
	maxSearchContextLines = 5
	// maxMatchLineChars 是结果里每行保留的最大字符数。
	maxMatchLineChars = 200
)

// errStopWalk 用于在达到结果或访问上限时提前结束遍历。
var errStopWalk = errors.New("stop")

// searchSkipDirs 是内容搜索默认跳过的目录名（依赖目录与版本控制元数据）。
var searchSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	".venv": true, "venv": true, "__pycache__": true, ".idea": true, ".vscode": true,
}

// searchFiles 按文件名通配与正则内容搜索工作区。
type searchFiles struct{}

// NewSearchFiles 创建 search_files 工具。
func NewSearchFiles() Tool { return searchFiles{} }

type searchFilesArgs struct {
	Glob          string `json:"glob"`
	Query         string `json:"query"`
	Path          string `json:"path"`
	CaseSensitive bool   `json:"case_sensitive"`
	MaxResults    int    `json:"max_results"`
	ContextLines  int    `json:"context_lines"`
}

type searchPlan struct {
	args  searchFilesArgs
	glob  *regexp.Regexp
	query *regexp.Regexp
}

func (searchFiles) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "search_files",
		Description: "在工作区内搜索文件：glob 按路径通配（支持 *、?、** 与 [...]），query 用正则表达式搜索文件内容；" +
			"两者至少提供一个，同时提供时只在匹配 glob 的文件里搜索内容。结果数量有上限，二进制与超大文件会被跳过。",
		InputSchema: objectSchema(map[string]any{
			"glob":           stringProp("路径通配，例如 *.go、src/**/*.ts；不含 \"/\" 时只匹配文件名"),
			"query":          stringProp("内容的正则表达式（RE2 语法）"),
			"path":           stringProp("搜索起点目录（工作区相对路径）；省略表示根目录"),
			"case_sensitive": boolProp("内容搜索是否区分大小写；默认不区分"),
			"max_results":    intProp("最多返回的结果数；默认 50", 1, maxSearchResults),
			"context_lines":  intProp(toolText(toolLocale(nil), "context_argument", nil), 0, maxSearchContextLines),
		}),
		Source: SourceBuiltin,
		Risk:   model.RiskRead,
	}
}

func (searchFiles) parse(raw json.RawMessage) (searchPlan, error) {
	var a searchFilesArgs
	if err := decodeArgs(raw, &a); err != nil {
		return searchPlan{}, err
	}
	a.Glob = strings.TrimSpace(a.Glob)
	a.Query = strings.TrimSpace(a.Query)
	a.Path = cleanRelPath(a.Path)
	if a.Glob == "" && a.Query == "" {
		return searchPlan{}, model.NewError(model.CodeInvalid, "glob 与 query 至少提供一个")
	}
	if a.ContextLines < 0 || a.ContextLines > maxSearchContextLines {
		return searchPlan{}, model.NewError(model.CodeInvalid, toolText(toolLocale(nil), "context_invalid", nil))
	}
	if a.MaxResults <= 0 {
		a.MaxResults = defaultSearchResults
	}
	if a.MaxResults > maxSearchResults {
		a.MaxResults = maxSearchResults
	}
	plan := searchPlan{args: a}
	if a.Glob != "" {
		re, err := globToRegexp(a.Glob)
		if err != nil {
			return searchPlan{}, model.Wrap(model.CodeInvalid, "glob 不合法", err)
		}
		plan.glob = re
	}
	if a.Query != "" {
		expr := a.Query
		if !a.CaseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return searchPlan{}, model.Wrap(model.CodeInvalid, "query 不是合法的正则表达式", err)
		}
		plan.query = re
	}
	return plan, nil
}

func (t searchFiles) Describe(raw json.RawMessage) (Description, error) {
	p, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	var parts []string
	if p.args.Glob != "" {
		parts = append(parts, "文件 "+p.args.Glob)
	}
	if p.args.Query != "" {
		parts = append(parts, "内容 /"+firstLine(p.args.Query, 60)+"/")
	}
	return Description{
		Summary: fmt.Sprintf("搜索 %s（目录 %s）", strings.Join(parts, "，"), p.args.Path),
		Details: map[string]any{"path": p.args.Path, "glob": p.args.Glob, "query": p.args.Query, "context_lines": p.args.ContextLines},
	}, nil
}

func (t searchFiles) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	p, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	startInfo, err := rc.Workspace.Stat(ctx, p.args.Path)
	if err != nil {
		return model.ToolResult{}, err
	}
	if !startInfo.IsDir {
		return model.ToolResult{}, model.Errorf(model.CodeInvalid, "%s 不是目录", p.args.Path)
	}
	startAbs, err := rc.Workspace.Resolve(p.args.Path)
	if err != nil {
		return model.ToolResult{}, err
	}
	root := rc.Workspace.Root()

	var (
		results  []string
		visited  int
		hitLimit bool
		stopped  string
	)
	walkErr := filepath.WalkDir(startAbs, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 无法进入的目录跳过，不让整次搜索失败。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relOS, err := filepath.Rel(root, abs)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relOS)
		if abs != startAbs {
			visited++
			if visited > maxSearchVisits {
				stopped = toolText(toolLocale(nil), "search_scan_limit", map[string]any{"limit": maxSearchVisits})
				return errStopWalk
			}
		}
		if d.IsDir() {
			if abs != startAbs && searchSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// 符号链接既不跟随也不匹配：它指向的内容不受工作区边界约束。
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		if p.glob != nil && !p.glob.MatchString(rel) {
			return nil
		}
		if p.query == nil {
			results = append(results, rel)
		} else {
			info, err := d.Info()
			if err != nil || info.Size() > maxSearchFileBytes {
				return nil
			}
			data, err := rc.Workspace.ReadFile(ctx, rel, maxSearchFileBytes)
			if err != nil || bytes.IndexByte(data, 0) >= 0 {
				return nil
			}
			results = append(results, matchLinesWithContext(rel, data, p.query, p.args.MaxResults-len(results), p.args.ContextLines)...)
		}
		if len(results) >= p.args.MaxResults {
			hitLimit = true
			return errStopWalk
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
		if ctx.Err() != nil {
			return model.ToolResult{}, ctxErr(ctx)
		}
		return model.ToolResult{}, model.Wrap(model.CodeInternal, "搜索失败", walkErr)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 条结果（目录 %s）\n", len(results), p.args.Path)
	for _, r := range results {
		b.WriteString(r)
		b.WriteString("\n")
	}
	if hitLimit {
		fmt.Fprintf(&b, "[结果已达到上限 %d 条，可能还有更多；请缩小范围或提高 max_results]\n", p.args.MaxResults)
	}
	if stopped != "" {
		b.WriteString(stopped)
		b.WriteString("\n")
	}
	return model.ToolResult{Content: b.String(), Truncated: hitLimit || stopped != ""}, nil
}

// matchLines 返回 data 中匹配 re 的行，格式为 "rel:行号: 文本"，最多 limit 条。
func matchLines(rel string, data []byte, re *regexp.Regexp, limit int) []string {
	return matchLinesWithContext(rel, data, re, limit, 0)
}

// matchLinesWithContext 每个匹配返回一段，匹配行用 ":"、上下文行用 "-" 标记；limit 只统计匹配。
func matchLinesWithContext(rel string, data []byte, re *regexp.Regexp, limit, contextLines int) []string {
	var out []string
	var previous [][]byte
	lineNo := 0
	for len(data) > 0 && len(out) < limit {
		lineNo++
		line, rest := takeSearchLine(data)
		if re.Match(line) {
			var block []string
			for i, before := range previous {
				block = append(block, formatSearchLine(rel, lineNo-len(previous)+i, before, "-"))
			}
			block = append(block, formatSearchLine(rel, lineNo, line, ":"))
			lookahead := rest
			for i := 1; i <= contextLines && len(lookahead) > 0; i++ {
				after, remaining := takeSearchLine(lookahead)
				block = append(block, formatSearchLine(rel, lineNo+i, after, "-"))
				lookahead = remaining
			}
			out = append(out, strings.Join(block, "\n"))
		}
		if contextLines > 0 {
			previous = append(previous, line)
			if len(previous) > contextLines {
				previous = previous[1:]
			}
		}
		data = rest
	}
	return out
}

func takeSearchLine(data []byte) (line, rest []byte) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return data[:index], data[index+1:]
	}
	return data, nil
}

func formatSearchLine(rel string, lineNo int, line []byte, separator string) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(line), "�"))
	if runes := []rune(text); len(runes) > maxMatchLineChars {
		text = string(runes[:maxMatchLineChars]) + "…"
	}
	return fmt.Sprintf("%s%s%d%s %s", rel, separator, lineNo, separator, text)
}

// globToRegexp 把路径通配转成正则：* 匹配除 "/" 外的任意字符，? 匹配一个，**/ 匹配零个或多个目录，
// ** 匹配任意内容，[...] 原样传递。不含 "/" 的模式只匹配文件名部分。
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	pattern = strings.TrimPrefix(pattern, "./")
	var b strings.Builder
	b.WriteString("^")
	if !strings.Contains(pattern, "/") {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end <= 1 {
				return nil, errors.New("字符类缺少 ]")
			}
			class := pattern[i+1 : i+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, "\\", "\\\\") + "]")
			i += end
		default:
			r, size := utf8.DecodeRuneInString(pattern[i:])
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += size - 1
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
