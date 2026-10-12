package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeText(t *testing.T) {
	cases := []struct {
		name, in, want string
		max            int
	}{
		{"去控制字符保留换行与制表符", "a\x00b\x1fc\td\ne\x7f", "abc\td\ne", 0},
		{"CRLF 归一为 LF", "一\r\n二\r三", "一\n二三", 0},
		{"去双向覆盖符", "abc‮def⁦g", "abcdefg", 0},
		{"保留零宽连接符等普通格式字符", "👩‍💻", "👩‍💻", 0},
		{"丢弃非法 UTF-8", "ok\xffbad", "okbad", 0},
		{"首尾空白被去掉", "  \n 内容 \t\n", "内容", 0},
		{"按字节上限在字符边界截断", "你好世界", "你好", 7},
		{"上限不足一个字符时为空", "你", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeText(c.in, c.max); got != c.want {
				t.Fatalf("SanitizeText(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}

	long := strings.Repeat("字", 5000)
	got := SanitizeText(long, 0)
	if len(got) > MaxInboundBytes || !utf8.ValidString(got) || len(got) < MaxInboundBytes-3 {
		t.Fatalf("默认上限应为 8 KiB 且保持合法 UTF-8，得到 %d 字节", len(got))
	}
}

func TestSplitTextShortAndEmpty(t *testing.T) {
	if got := SplitText("", 10); got != nil {
		t.Fatalf("空文本应返回 nil，得到 %v", got)
	}
	if got := SplitText("短文本", 10); len(got) != 1 || got[0] != "短文本" {
		t.Fatalf("不超限的文本应原样返回，得到 %v", got)
	}
	text := strings.Repeat("a", DefaultMaxChunk+1)
	if got := SplitText(text, 0); len(got) != 2 {
		t.Fatalf("上限不为正时应用默认值 %d，得到 %d 片", DefaultMaxChunk, len(got))
	}
}

func TestSplitTextPrefersParagraphThenLineThenSpace(t *testing.T) {
	text := "第一段第一行\n第一段第二行\n\n第二段\n第二段第二行 还有词"
	got := SplitText(text, 16)
	for i, c := range got {
		if n := utf8.RuneCountInString(c); n > 16 {
			t.Fatalf("第 %d 片超过上限: %d", i, n)
		}
	}
	if got[0] != "第一段第一行\n第一段第二行" {
		t.Fatalf("应优先在空行处切分，得到 %q", got[0])
	}
	joined := strings.ReplaceAll(strings.Join(got, ""), "\n", "")
	if joined != strings.ReplaceAll(text, "\n", "") {
		t.Fatalf("切分不应丢失内容，得到 %q", joined)
	}

	words := SplitText("alpha beta gamma delta", 12)
	if len(words) != 2 || words[0] != "alpha beta " || words[1] != "gamma delta" {
		t.Fatalf("应在空格处切分，得到 %q", words)
	}
	hard := SplitText(strings.Repeat("x", 25), 10)
	if len(hard) != 3 || hard[0] != strings.Repeat("x", 10) || hard[2] != strings.Repeat("x", 5) {
		t.Fatalf("没有边界时应硬切，得到 %q", hard)
	}
}

func TestSplitTextKeepsCodeFencesBalanced(t *testing.T) {
	var code strings.Builder
	for i := 0; i < 30; i++ {
		code.WriteString("line of code\n")
	}
	text := "说明：\n```go\n" + code.String() + "```\n结尾"
	got := SplitText(text, 100)
	if len(got) < 3 {
		t.Fatalf("应切成多片，得到 %d", len(got))
	}
	for i, c := range got {
		if n := utf8.RuneCountInString(c); n > 100 {
			t.Fatalf("第 %d 片超过上限: %d", i, n)
		}
		if countFenceLines(c)%2 != 0 {
			t.Fatalf("第 %d 片的代码围栏不平衡:\n%s", i, c)
		}
	}
	if !strings.HasSuffix(got[0], "\n```") {
		t.Fatalf("第一片应补闭合围栏，得到 %q", got[0])
	}
	if !strings.HasPrefix(got[1], "```\n") {
		t.Fatalf("第二片应补开启围栏，得到 %q", got[1])
	}
	if !strings.HasSuffix(got[len(got)-1], "结尾") {
		t.Fatalf("最后一片应保留结尾，得到 %q", got[len(got)-1])
	}
	body := strings.Join(got, "\n")
	if strings.Count(body, "line of code") != 30 {
		t.Fatalf("代码行数应完整保留，得到 %d", strings.Count(body, "line of code"))
	}
}

func TestReplaceTokens(t *testing.T) {
	got := replaceTokens("处理 {summary}（{risk}）{unknown}", map[string]string{"summary": "写文件", "risk": "write"})
	if got != "处理 写文件（write）{unknown}" {
		t.Fatalf("占位符替换错误: %q", got)
	}
}

func TestRunFailedTextLocalizesKnownReasonsAndHidesOthers(t *testing.T) {
	texts := Texts{RunErrorMap: map[string]string{"run timeout": "运行超时", "daemon restarted": "守护进程重启，运行已中断"}}.withDefaults()
	if got := texts.runFailedText("run timeout"); got != "处理失败：运行超时" {
		t.Errorf("已知常量应换成本地化原因，得到 %q", got)
	}
	if got := texts.runFailedText("daemon restarted"); got != "处理失败：守护进程重启，运行已中断" {
		t.Errorf("已知常量应换成本地化原因，得到 %q", got)
	}
	for _, raw := range []string{"persist failed", "upstream 401: invalid key sk-xxx", ""} {
		if got := texts.runFailedText(raw); got != "处理失败" {
			t.Errorf("未命中的原因 %q 不应原样外发，应只发通用文案，得到 %q", raw, got)
		}
	}
	// 组合根传入的 map 被复制：之后修改不影响网关。
	src := map[string]string{"run timeout": "超时"}
	copied := Texts{RunErrorMap: src}.withDefaults()
	src["run timeout"] = "改了"
	if got := copied.runFailedText("run timeout"); got != "处理失败：超时" {
		t.Errorf("RunErrorMap 应被复制，得到 %q", got)
	}
}

func TestWithoutToken(t *testing.T) {
	cases := map[string]string{
		"处理失败：{error}":               "处理失败",
		"Processing failed: {error}": "Processing failed",
		"处理失败（{error}），请稍后重试":        "处理失败，请稍后重试",
		"Failed [{error}] - retry":   "Failed - retry",
		"{error}: failed":            "failed",
		"没有占位符":                      "没有占位符",
		"{error}":                    "",
	}
	for in, want := range cases {
		if got := withoutToken(in, "error"); got != want {
			t.Errorf("withoutToken(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 模板只有占位符时退回默认文案的通用形式。
	if got := (Texts{RunFailed: "{error}"}).withDefaults().runFailedText("x"); got != "处理失败" {
		t.Errorf("模板去掉占位符后为空时应退回默认通用文案，得到 %q", got)
	}
}
