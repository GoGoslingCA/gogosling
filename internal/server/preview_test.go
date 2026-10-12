package server_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
)

func Test预览端点鉴权下载范围文件变化和错误隔离(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("阅读助手")
	ws, e := h.env.Deps.Workspaces.Open(context.Background(), a.ID, a.WorkspaceDir)
	if e != nil {
		t.Fatal(e)
	}
	if e = ws.WriteFile(context.Background(), "plan.md", []byte("# 计划\n实际内容")); e != nil {
		t.Fatal(e)
	}
	base := "/api/v1/agents/" + a.ID
	rec := h.do("GET", base+"/file-preview?path=plan.md", nil)
	mustStatus(t, rec, 200)
	doc := decodeBody[preview.Document](t, rec)
	if doc.Kind != "markdown" || !strings.Contains(doc.Text, "实际内容") {
		t.Fatal("预览应保留原文")
	}
	rec = h.do("GET", base+"/file-content?path=plan.md&version="+url.QueryEscape(doc.Version), nil, withHeader("Range", "bytes=0-1"))
	mustStatus(t, rec, 206)
	if rec.Body.String() != "# " || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("Range和安全下载头错误")
	}
	_ = ws.WriteFile(context.Background(), "plan.md", []byte("外部已修改"))
	mustError(t, h.do("GET", base+"/file-content?path=plan.md&version="+url.QueryEscape(doc.Version), nil), 409, model.CodeConflict)
	mustError(t, h.do("GET", base+"/file-preview?path=../daemon.json", nil), 403, model.CodeForbidden)
	_ = ws.WriteFile(context.Background(), "broken.docx", []byte("not zip"))
	bad := mustError(t, h.do("GET", base+"/file-preview?path=broken.docx", nil), 400, model.CodeInvalid)
	if bad.Error.Details["reason"] != "preview_corrupt" {
		t.Fatal("损坏格式应返回统一原因")
	}
	mustStatus(t, h.do("GET", base+"/file-preview?path=plan.md", nil), 200)
	mustStatus(t, h.do("GET", base+"/files", nil), 200)
	mustError(t, h.do("GET", base+"/file-layout?path=plan.md", nil), 503, model.CodeUnavailable)
}

func Test旧内核不声明预览且请求返回可恢复错误(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.env.WithoutWorkbench = true })
	a := h.seedAgent("旧版助手")
	rec := h.do("GET", "/api/v1/version", nil)
	mustStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "workbench.preview") {
		t.Fatal("旧能力不能出现预览入口")
	}
	mustError(t, h.do("GET", "/api/v1/agents/"+a.ID+"/file-preview?path=a.md", nil), 503, model.CodeUnavailable)
}
