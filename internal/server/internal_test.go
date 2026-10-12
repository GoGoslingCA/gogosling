package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

func TestStatusForCoversEveryCode(t *testing.T) {
	want := map[model.Code]int{
		model.CodeInvalid:      400,
		model.CodeUnauthorized: 401,
		model.CodeForbidden:    403,
		model.CodeDenied:       403,
		model.CodeNotFound:     404,
		model.CodeConflict:     409,
		model.CodeCancelled:    409,
		model.CodeUnavailable:  503,
		model.CodeTimeout:      504,
		model.CodeInternal:     500,
	}
	if len(want) != len(i18n.ErrorCodes) {
		t.Fatalf("映射表覆盖 %d 个错误码，i18n 镜像了 %d 个，期望一致", len(want), len(i18n.ErrorCodes))
	}
	for _, c := range i18n.ErrorCodes {
		got, ok := want[model.Code(c)]
		if !ok {
			t.Fatalf("i18n.ErrorCodes 中的 %q 不在映射表里", c)
		}
		if s := statusFor(model.Code(c)); s != got {
			t.Errorf("错误码 %s 映射为 %d，期望 %d", c, s, got)
		}
	}
	if statusFor("weird") != 500 {
		t.Error("未知错误码应映射为 500")
	}
}

func TestNormalizeOrigin(t *testing.T) {
	ok := map[string]string{
		"http://localhost:5173":   "http://localhost:5173",
		"HTTP://LocalHost:5173/":  "http://localhost:5173",
		"https://127.0.0.1":       "https://127.0.0.1",
		"http://[::1]:3000":       "http://[::1]:3000",
		" https://dev.example.cn": "https://dev.example.cn",
	}
	for in, want := range ok {
		got, err := normalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("normalizeOrigin(%q) = %q, %v；期望 %q", in, got, err, want)
		}
	}
	bad := []string{"", "null", "app://gogosling", "ftp://x", "http://", "http://u:p@host",
		"http://host/path", "http://host?q=1", "http://host#f", "localhost:5173"}
	for _, in := range bad {
		if got, err := normalizeOrigin(in); err == nil {
			t.Errorf("normalizeOrigin(%q) = %q，期望报错", in, got)
		}
	}
}

func TestAllowedHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "127.0.0.1:46750", "localhost", "LOCALHOST:1", "[::1]:9", "[::1]",
		"127.0.0.2:1", "127.1.2.3"} {
		if !allowedHost(h) {
			t.Errorf("Host %q 应被接受", h)
		}
	}
	for _, h := range []string{"", ":80", "[]:1", "evil.com", "evil.com:46750", "localhost.:1",
		"localhost.evil.com", "0.0.0.0:1", "[::2]:1", "192.168.1.2", "128.0.0.1:1"} {
		if allowedHost(h) {
			t.Errorf("Host %q 应被拒绝", h)
		}
	}
}

func TestCheckLoopbackAddr(t *testing.T) {
	for _, a := range []string{"127.0.0.1:0", "localhost:46750", "[::1]:0", "127.0.0.2:0", "127.255.255.254:80"} {
		if err := checkLoopbackAddr(a); err != nil {
			t.Errorf("地址 %q 应被接受: %v", a, err)
		}
	}
	for _, a := range []string{"0.0.0.0:0", ":46750", "192.168.1.2:80", "example.com:80", "127.0.0.1", "127.0.0.1:x", "127.0.0.1:70000"} {
		if err := checkLoopbackAddr(a); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("地址 %q 应返回 invalid，实际 %v", a, err)
		}
	}
}

func TestIsUpload(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/v1/sessions/S1/attachments", true},
		{http.MethodGet, "/api/v1/sessions/S1/attachments", false},
		{http.MethodPost, "/api/v1/sessions//attachments", false},
		{http.MethodPost, "/api/v1/sessions/S1/messages", false},
		{http.MethodPost, "/api/v1/sessions/S1/attachments/x", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := isUpload(r); got != c.want {
			t.Errorf("isUpload(%s %s) = %v，期望 %v", c.method, c.path, got, c.want)
		}
	}
}

func TestOptionalDistinguishesAbsentNullAndValue(t *testing.T) {
	var v struct {
		A optional[[]string] `json:"a"`
		B optional[[]string] `json:"b"`
		C optional[[]string] `json:"c"`
		D optional[[]string] `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"b":null,"c":[],"d":["x"]}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A.Set {
		t.Error("缺席的字段不应标记为已设置")
	}
	if !v.B.Set || !v.B.Null {
		t.Error("null 应标记为已设置且为 null")
	}
	if !v.C.Set || v.C.Null || v.C.Value == nil || len(v.C.Value) != 0 {
		t.Errorf("[] 应得到非 nil 的空切片，实际 %#v", v.C)
	}
	if !v.D.Set || len(v.D.Value) != 1 || v.D.Value[0] != "x" {
		t.Errorf("数组值解码错误: %#v", v.D)
	}
	if p := sliceField(v.A); p != nil {
		t.Error("缺席应映射为 nil 指针（不修改）")
	}
	if p := sliceField(v.B); p == nil || *p != nil {
		t.Error("null 应映射为指向 nil 切片的指针（重置为默认）")
	}
	if p := sliceField(v.C); p == nil || *p == nil || len(*p) != 0 {
		t.Error("[] 应映射为指向空切片的指针（清空）")
	}
}

func TestLocalizeRunError(t *testing.T) {
	if got := localizeRunError("en", "run timeout"); got != i18n.T("en", "run.error.run_timeout", nil) {
		t.Errorf("run timeout 未映射为英文文案: %q", got)
	}
	if got := localizeRunError("zh", "daemon restarted"); got != i18n.T("zh", "run.error.daemon_restarted", nil) {
		t.Errorf("daemon restarted 未映射为中文文案: %q", got)
	}
	if got := localizeRunError("en", "upstream 502"); got != "upstream 502" {
		t.Errorf("非机器常量应原样返回，实际 %q", got)
	}
	if got := localizeRunError("en", "worker exited: exit status 1"); got != "worker exited: exit status 1" {
		t.Errorf("运行时给出的自由文本应原样返回，实际 %q", got)
	}
}

// TestRunErrorConstantsAreLocalized 覆盖 C19：session 与 engine 写入 Run.Error 的机器常量都有 run.error.* 文案，
// 不会把英文常量原样展示给中文用户。
func TestRunErrorConstantsAreLocalized(t *testing.T) {
	consts := []string{
		session.ErrorDaemonRestarted, session.ErrorRunTimeout, session.ErrorPersistFailed, session.ErrorRunFailed,
		engine.ErrorWorkerUnavailable, engine.ErrorWorkerCrashed, engine.ErrorWorkerStopped,
		engine.ErrorRunRejected, engine.ErrorRunCancelled, engine.ErrorInvalidRequest,
	}
	for _, c := range consts {
		for _, loc := range i18n.Locales() {
			got := localizeRunError(loc, c)
			want, ok := i18n.RunErrorText(loc, c)
			if !ok || got != want || got == c {
				t.Errorf("常量 %q 在 %s 下应本地化为 run.error.* 文案，实际 %q", c, loc, got)
			}
		}
	}
	if zh, en := localizeRunError("zh", engine.ErrorWorkerCrashed), localizeRunError("en", engine.ErrorWorkerCrashed); zh == en {
		t.Errorf("worker crashed 的中英文文案应不同，都是 %q", zh)
	}
}
