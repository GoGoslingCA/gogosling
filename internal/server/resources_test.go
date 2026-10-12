package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

func Test资源设置保存校验与鉴权(t *testing.T) {
	h := newHarness(t)
	if err := h.env.Deps.Sessions.SetParallelLimit(2); err != nil {
		t.Fatal(err)
	}
	if got := h.do("GET", "/api/v1/resources", nil, withoutAuth()).Code; got != http.StatusUnauthorized {
		t.Fatalf("未授权读取应返回401，实际%d", got)
	}
	for _, body := range []string{`{}`, `{"max_parallel":0}`, `{"max_parallel":9}`, `{"max_parallel":1.5}`, `{"max_parallel":2,"extra":true}`} {
		if got := h.do("PUT", "/api/v1/resources", body).Code; got != http.StatusBadRequest {
			t.Fatalf("非法设置应返回400，实际%d", got)
		}
	}
	rec := h.do("PUT", "/api/v1/resources", `{"max_parallel":1}`)
	if rec.Code != 200 {
		t.Fatalf("保存失败：%s", rec.Body.String())
	}
	var state session.ResourceState
	_ = json.Unmarshal(rec.Body.Bytes(), &state)
	saved, err := h.env.Store.GetSetting(context.Background(), server.SettingMaxParallel)
	if state.MaxParallel != 1 || err != nil || saved != "1" {
		t.Fatalf("内核与持久值应一致：%+v %q %v", state, saved, err)
	}
	if got := h.do("PUT", "/api/v1/resources", `{"max_parallel":2}`, withoutAuth()).Code; got != 401 {
		t.Fatal("修改也必须鉴权")
	}
}
func Test旧装配不宣告资源能力且拒绝修改(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{"GET", "PUT"} {
		body := ""
		if method == "PUT" {
			body = `{"max_parallel":2}`
		}
		if rec := h.do(method, "/api/v1/resources", body); rec.Code != 503 {
			t.Fatalf("无能力时应返回503：%d %s", rec.Code, rec.Body.String())
		}
	}
	if h.env.Deps.Sessions.Resources().MaxParallel != 0 {
		t.Fatal("无能力请求不能静默打开新模式")
	}
}
