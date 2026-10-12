package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/config"
)

func TestBuildEnvAppliesAllowlist(t *testing.T) {
	parent := map[string]string{
		"PATH":           "/usr/bin",
		"HOME":           "/home/u",
		"OPENAI_API_KEY": "sk-secret",
		"GOGOSLING_HOME": "/data",
		"LANG":           "C",
	}
	lookup := func(k string) (string, bool) {
		v, ok := parent[k]
		return v, ok
	}
	env := buildEnv("en", map[string]string{"PYTHONHOME": "/opt/py", "LANG": "zh_CN.UTF-8"}, lookup)

	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nPATH=/usr/bin\n", "\nHOME=/home/u\n",
		"\nGOGOSLING_PROTOCOL_VERSION=1\n", "\nGOGOSLING_LOCALE=en\n",
		"\nPYTHONUNBUFFERED=1\n", "\nPYTHONIOENCODING=utf-8\n",
		"\nPYTHONHOME=/opt/py\n", "\nLANG=zh_CN.UTF-8\n",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少 %q，实际: %v", strings.TrimSpace(want), env)
		}
	}
	for _, forbidden := range []string{"OPENAI_API_KEY", "GOGOSLING_HOME", "LANG=C\n"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("不应透传 %q，实际: %v", forbidden, env)
		}
	}
	if count := strings.Count(joined, "\nLANG="); count != 1 {
		t.Errorf("LANG 应只出现一次（被覆盖），实际 %d 次", count)
	}
}

func TestBuildEnvPassesProxyAndCertificateSettings(t *testing.T) {
	parent := map[string]string{
		"https_proxy":   "http://user:pw@127.0.0.1:7890",
		"HTTP_PROXY":    "http://127.0.0.1:7890",
		"NO_PROXY":      "localhost,127.0.0.1",
		"ALL_PROXY":     "socks5://127.0.0.1:1080",
		"SSL_CERT_FILE": "/etc/corp-ca.pem",
		"SSL_CERT_DIR":  "/etc/ssl/certs",
		// 不在白名单里的同类变量仍不透传。
		"FTP_PROXY": "http://x",
	}
	lookup := func(k string) (string, bool) {
		v, ok := parent[k]
		return v, ok
	}
	env := buildEnv("zh", nil, lookup)
	for _, want := range []string{
		"https_proxy=http://user:pw@127.0.0.1:7890", "HTTP_PROXY=http://127.0.0.1:7890",
		"NO_PROXY=localhost,127.0.0.1", "ALL_PROXY=socks5://127.0.0.1:1080",
		"SSL_CERT_FILE=/etc/corp-ca.pem", "SSL_CERT_DIR=/etc/ssl/certs",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("工作进程环境应包含 %q（否则 httpx 拿不到代理/证书设置），实际: %v", want, env)
		}
	}
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "FTP_PROXY=") }) {
		t.Errorf("白名单之外的变量不应透传: %v", env)
	}
}

func TestBuildEnvUsesSharedAllowlist(t *testing.T) {
	// 白名单的唯一定义在 config；engine 不得再维护一份本地副本。
	lookup := func(k string) (string, bool) { return "v-" + k, true }
	env := buildEnv("en", nil, lookup)
	for _, k := range config.ChildEnvAllowlist() {
		if !slices.Contains(env, k+"=v-"+k) {
			t.Errorf("config.ChildEnvAllowlist 中的 %s 应透传给工作进程", k)
		}
	}
}

func TestRedactProxyURL(t *testing.T) {
	cases := map[string]string{
		"http://user:pw@127.0.0.1:7890":     "http://***@127.0.0.1:7890",
		"http://user:p@ss/w0rd@proxy:8080/": "http://***@proxy:8080/",
		"user:pw@proxy:8080":                "***@proxy:8080",
		"socks5://tok@h:1080":               "socks5://***@h:1080",
		"http://127.0.0.1:7890":             "http://127.0.0.1:7890",
		"":                                  "",
	}
	for in, want := range cases {
		if got := redactProxyURL(in); got != want {
			t.Errorf("redactProxyURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestEgressSummaryRedactsUserinfo(t *testing.T) {
	env := []string{
		"PATH=/bin", "HOME=/h",
		"https_proxy=http://alice:s3cret@10.0.0.1:3128",
		"NO_PROXY=localhost",
		"SSL_CERT_FILE=/ca.pem",
		"GOGOSLING_LOCALE=en",
	}
	got := egressSummary(env)
	want := []string{"https_proxy=http://***@10.0.0.1:3128", "NO_PROXY=localhost", "SSL_CERT_FILE=/ca.pem"}
	if !slices.Equal(got, want) {
		t.Errorf("egressSummary = %q，期望 %q", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "s3cret") {
		t.Errorf("日志摘要不得包含代理口令: %q", got)
	}
}
