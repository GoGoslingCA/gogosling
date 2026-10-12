package i18n

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]struct {
		loc string
		ok  bool
	}{
		"zh":          {"zh", true},
		"ZH-cn":       {"zh", true},
		"zh_TW":       {"zh", true},
		"zh-Hans-CN":  {"zh", true},
		"en":          {"en", true},
		"en-US":       {"en", true},
		" en-GB ":     {"en", true},
		"fr":          {"fr", true},
		"fr-FR":       {"fr", true},
		"fr_CA":       {"fr", true},
		"":            {"", false},
		"zhx":         {"", false},
		"english":     {"", false},
		"ja-JP":       {"", false},
		"zh-CN;q=0.9": {"", false},
	}
	for in, want := range cases {
		loc, ok := Normalize(in)
		if loc != want.loc || ok != want.ok {
			t.Errorf("Normalize(%q) = (%q, %v)，期望 (%q, %v)", in, loc, ok, want.loc, want.ok)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	cases := map[string]struct {
		loc string
		ok  bool
	}{
		"zh-CN,zh;q=0.9,en;q=0.8":    {"zh", true},
		"en-US,en;q=0.9":             {"en", true},
		"fr-FR,fr;q=0.9,zh;q=0.8":    {"fr", true},
		"en;q=0.1,zh;q=0.9":          {"zh", true}, // 按 q 值排序而不是出现顺序
		"zh;q=0,en;q=0.5":            {"en", true}, // q=0 表示不接受
		"*":                          {"", false},
		"fr, de":                     {"fr", true},
		"ja, de":                     {"", false},
		"":                           {"", false},
		"  ":                         {"", false},
		"zh ; q=0.5 , en ; q=0.5":    {"zh", true}, // 同 q 值保持原顺序
		"en;q=abc,zh;q=0.5":          {"en", true}, // 无法解析的 q 按 1 处理
		"ja,zh-TW;Q=0.7,en-GB;q=0.3": {"zh", true},
	}
	for in, want := range cases {
		loc, ok := FromAcceptLanguage(in)
		if loc != want.loc || ok != want.ok {
			t.Errorf("FromAcceptLanguage(%q) = (%q, %v)，期望 (%q, %v)", in, loc, ok, want.loc, want.ok)
		}
	}
}

func TestResolveLocalePriority(t *testing.T) {
	cases := []struct {
		pref, accept, channel, want string
	}{
		{"zh", "en", "telegram", "zh"},           // 偏好优先
		{"", "zh-CN,en;q=0.5", "telegram", "zh"}, // 其次 Accept-Language
		{"", "", "feishu", "zh"},                 // 再次通道提示
		{"", "", "discord", "en"},
		{"", "", "console", "en"}, // 未知通道 → 默认
		{"", "", "", "en"},
		{"fr", "", "wecom", "fr"}, // 法语偏好优先于通道提示
		{"ja", "", "wecom", "zh"}, // 不支持的偏好被跳过，而不是误判为 en
		{"fr", "ja", "", "fr"},
		{"ja", "de", "", "en"},
		{"EN_us", "zh", "feishu", "en"},
		{"", "ja,de;q=0.9", "DingTalk", "zh"}, // 不支持的语言被跳过，通道提示不区分大小写
		{"", "fr,de;q=0.9", "DingTalk", "fr"}, // 法语 Accept-Language 优先于通道提示
	}
	for _, c := range cases {
		if got := ResolveLocale(c.pref, c.accept, c.channel); got != c.want {
			t.Errorf("ResolveLocale(%q, %q, %q) = %q，期望 %q", c.pref, c.accept, c.channel, got, c.want)
		}
	}
}

// envOf 把 map 包装成 getenv。
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestEffectiveLocalePriority(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		env     map[string]string
		want    string
	}{
		{"设置优先于环境", "zh", map[string]string{"LANG": "en_US.UTF-8"}, "zh"},
		{"设置规范化", "EN-gb", map[string]string{"LANG": "zh_CN.UTF-8"}, "en"},
		{"未设置时看 LANG", "", map[string]string{"LANG": "zh_CN.UTF-8"}, "zh"},
		{"LC_ALL 优先于 LANG", "", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, "en"},
		{"LC_MESSAGES 优先于 LANG", "", map[string]string{"LC_MESSAGES": "zh_TW", "LANG": "en_US"}, "zh"},
		{"不支持的 LC_ALL 被跳过", "", map[string]string{"LC_ALL": "C", "LANG": "zh_CN@hans"}, "zh"},
		{"法语设置优先于环境", "fr", map[string]string{"LANG": "zh_CN.UTF-8"}, "fr"},
		{"不支持的设置被跳过", "ja", map[string]string{"LANG": "zh_CN.UTF-8"}, "zh"},
		{"没有任何信号时用默认语言", "", nil, DefaultLocale},
		{"全是不支持的值时用默认语言", "ja", map[string]string{"LANG": "POSIX"}, DefaultLocale},
	}
	for _, c := range cases {
		if got := EffectiveLocale(c.setting, envOf(c.env)); got != c.want {
			t.Errorf("%s：EffectiveLocale(%q, %v) = %q，期望 %q", c.name, c.setting, c.env, got, c.want)
		}
	}
	if got := EffectiveLocale("", nil); got != DefaultLocale {
		t.Errorf("getenv 为 nil 时应返回默认语言，得到 %q", got)
	}
}

func TestFromEnv(t *testing.T) {
	if _, ok := FromEnv(nil); ok {
		t.Error("getenv 为 nil 时应返回 false")
	}
	if _, ok := FromEnv(envOf(map[string]string{"LANG": "C.UTF-8"})); ok {
		t.Error("C 语言环境不应被识别为受支持的语言")
	}
	if loc, ok := FromEnv(envOf(map[string]string{"LANG": " en_GB.ISO-8859-1 "})); !ok || loc != "en" {
		t.Errorf("FromEnv 应识别 en_GB，得到 (%q, %v)", loc, ok)
	}
	if loc, ok := FromEnv(envOf(map[string]string{"LANG": "fr_FR.UTF-8"})); !ok || loc != "fr" {
		t.Errorf("FromEnv 应识别 fr_FR，得到 (%q, %v)", loc, ok)
	}
}
