package sandbox

import (
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 白名单只在 config 定义一份；本包在其上只加执行 shell 所需的增量，不再维护自己的基础副本。
func TestEnvAllowlistComesFromConfig(t *testing.T) {
	keys := map[string]int{}
	for _, k := range BaseEnvKeys() {
		keys[k]++
	}
	for _, k := range config.ChildEnvAllowlist() {
		if keys[k] != 1 {
			t.Errorf("子进程白名单应恰好包含一次 config.ChildEnvAllowlist 的 %s，得到 %d 次", k, keys[k])
		}
	}
	for _, k := range shellEnvExtras {
		if keys[k] != 1 {
			t.Errorf("子进程白名单应包含 shell 增量变量 %s", k)
		}
	}
	if len(keys) != len(BaseEnvKeys()) {
		t.Errorf("白名单不应有重复项: %v", BaseEnvKeys())
	}

	// 代理与证书变量（config 白名单的网络出口部分）应透传；白名单外的变量不透传。
	lookup := func(k string) (string, bool) {
		switch k {
		case "HTTPS_PROXY":
			return "http://127.0.0.1:7890", true
		case "SSL_CERT_FILE":
			return "/etc/ca.pem", true
		case "ComSpec":
			return `C:\Windows\system32\cmd.exe`, true
		case "OPENAI_API_KEY":
			return "leak", true
		}
		return "", false
	}
	joined := strings.Join(buildEnv(nil, lookup), "\n")
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:7890", "SSL_CERT_FILE=/etc/ca.pem", `ComSpec=C:\Windows\system32\cmd.exe`} {
		if !strings.Contains(joined, want) {
			t.Errorf("环境应包含 %s，得到 %q", want, joined)
		}
	}
	if strings.Contains(joined, "OPENAI_API_KEY") {
		t.Error("白名单之外的变量不应透传")
	}
}

func TestNormalizeRejectsNULInRawCommandLine(t *testing.T) {
	_, err := normalize(Spec{Command: []string{"cmd.exe"}, RawCommandLine: "cmd.exe /c \x00", Dir: t.TempDir()})
	if !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("命令行含 NUL 应为 CodeInvalid，得到 %v", err)
	}
}
