package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 端到端测试：传输层的 server 与 client 按依赖规则不能互相导入，
// 因此构建 internal/server/servertest/testdaemon 并作为子进程启动，经 daemon.json 发现后
// 对真实的服务端（真实 SQLite、真实领域服务、假推理运行时）调用客户端的全部方法。

const daemonPkg = "./internal/server/servertest/testdaemon"

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// buildTestDaemon 用当前工具链构建 testdaemon；找不到 go 命令时跳过。
func buildTestDaemon(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", exeName("go"))
		if _, serr := os.Stat(goBin); serr != nil {
			t.Skip("找不到 go 工具链，跳过端到端测试")
		}
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), exeName("testdaemon"))
	cmd := exec.Command(goBin, "build", "-o", out, daemonPkg)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 testdaemon 失败: %v\n%s", err, output)
	}
	return out
}

type daemon struct {
	dir    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *lockedBuffer
	// done 在子进程退出后关闭。
	done chan struct{}
}

// waitExit 等待子进程退出（例如经 POST /api/v1/system/shutdown 关闭之后）。
func (d *daemon) waitExit(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(timeout):
		t.Fatalf("testdaemon 没有在 %s 内退出\n%s", timeout, d.stderr.String())
	}
}

// lockedBuffer 收集子进程的标准错误；exec 在独立 goroutine 中写入，读取需要加锁。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startDaemon 启动 testdaemon 并等待 READY；测试结束时关闭标准输入让它优雅退出。
func startDaemon(t *testing.T, bin string, busCapacity int) *daemon {
	t.Helper()
	d := &daemon{dir: t.TempDir(), stderr: &lockedBuffer{}, done: make(chan struct{})}
	d.cmd = exec.Command(bin, "-data", d.dir, "-bus", strconv.Itoa(busCapacity))
	var err error
	if d.stdin, err = d.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := d.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	d.cmd.Stderr = d.stderr
	if err := d.cmd.Start(); err != nil {
		t.Fatalf("启动 testdaemon 失败: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "READY" {
			err = fmt.Errorf("意外的输出 %q", line)
		}
		ready <- err
		_, _ = io.Copy(io.Discard, stdout)
	}()
	t.Cleanup(func() {
		_ = d.stdin.Close()
		select {
		case <-d.done:
		case <-time.After(20 * time.Second):
			_ = d.cmd.Process.Kill()
			<-d.done
			t.Errorf("testdaemon 没有在关闭标准输入后退出")
		}
	})
	go func() {
		_ = d.cmd.Wait()
		close(d.done)
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("testdaemon 未就绪: %v\n%s", err, d.stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("等待 testdaemon 就绪超时\n%s", d.stderr.String())
	}
	return d
}

// tcpProxy 是可以切断与暂停的 TCP 转发，用来模拟事件流断线。
type tcpProxy struct {
	ln       net.Listener
	target   string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	blocked  bool
	accepted int
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.cut(true)
	})
	return p
}

func (p *tcpProxy) url() string { return "http://" + p.ln.Addr().String() }

func (p *tcpProxy) serve() {
	for {
		down, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.blocked {
			p.mu.Unlock()
			_ = down.Close()
			continue
		}
		p.accepted++
		p.mu.Unlock()
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = down.Close()
			continue
		}
		p.mu.Lock()
		p.conns[down], p.conns[up] = struct{}{}, struct{}{}
		p.mu.Unlock()
		go p.pipe(down, up)
		go p.pipe(up, down)
	}
}

func (p *tcpProxy) pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
	p.mu.Lock()
	delete(p.conns, dst)
	delete(p.conns, src)
	p.mu.Unlock()
}

// cut 断开所有现有连接；block 为 true 时之后的新连接也被立即关闭。
func (p *tcpProxy) cut(block bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = block
	for c := range p.conns {
		_ = c.Close()
	}
}

func (p *tcpProxy) accepts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted
}

// eventLog 在后台收集事件流，测试按条件等待。
type eventLog struct {
	mu   sync.Mutex
	evs  []event.Event
	errs []error
}

func (l *eventLog) collect(evs <-chan event.Event, errs <-chan error) {
	go func() {
		for err := range errs {
			l.mu.Lock()
			l.errs = append(l.errs, err)
			l.mu.Unlock()
		}
	}()
	go func() {
		for ev := range evs {
			l.mu.Lock()
			l.evs = append(l.evs, ev)
			l.mu.Unlock()
		}
	}()
}

func (l *eventLog) waitFor(t *testing.T, what string, pred func(event.Event) bool) event.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		for _, ev := range l.evs {
			if pred(ev) {
				l.mu.Unlock()
				return ev
			}
		}
		l.mu.Unlock()
		if time.Now().After(deadline) {
			l.mu.Lock()
			defer l.mu.Unlock()
			t.Fatalf("等待事件超时: %s（已收到 %d 条，错误 %v）", what, len(l.evs), l.errs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ofType(typ, runID string) func(event.Event) bool {
	return func(ev event.Event) bool { return ev.Type == typ && (runID == "" || ev.RunID == runID) }
}

func must[T any](t *testing.T, what string) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("%s 失败: %v", what, err)
		}
		return v
	}
}

func mustOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s 失败: %v", what, err)
	}
}

func TestEndToEndAgainstRealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式跳过端到端测试")
	}
	bin := buildTestDaemon(t)
	const busCapacity = 16
	d := startDaemon(t, bin, busCapacity)
	ctx := context.Background()

	c, err := Discover(ctx, d.dir, WithLocale("en"))
	if err != nil {
		t.Fatalf("Discover 失败: %v\n%s", err, d.stderr.String())
	}
	handle := must[identity.Handle](t, "读取 daemon.json")(identity.NewHandleFile(d.dir).Read(ctx))

	// 事件流经可切断的代理连接；从总线为空时开始订阅。
	proxy := newTCPProxy(t, strings.TrimPrefix(handle.Endpoint, "http://"))
	streamClient := New(proxy.url(), handle.Token, WithLocale("en"), WithReconnectBackoff(10*time.Millisecond, 50*time.Millisecond))
	sctx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	log := &eventLog{}
	log.collect(streamClient.Events(sctx, 0, ""))

	// ── system / settings ──
	mustOK(t, "Health", c.Health(ctx))
	ver := must[*VersionInfo](t, "Version")(c.Version(ctx))
	if ver.Version == "" || ver.Worker.Status != "unavailable" || ver.StartedAt == "" {
		t.Fatalf("版本信息 = %+v", ver)
	}
	st := must[*Settings](t, "GetSettings")(c.GetSettings(ctx))
	// 从未保存过语言：locale 省略，effective_locale 给出内核实际使用的语言。
	if st.Locale != "" || st.EffectiveLocale == "" || st.AllowedOrigins == nil {
		t.Fatalf("缺省设置 = %+v", st)
	}
	st = must[*Settings](t, "UpdateSettings")(c.UpdateSettings(ctx, SettingsPatch{
		Timezone: Ptr("Asia/Shanghai"), AllowedOrigins: &[]string{"http://localhost:5173"},
	}))
	if st.Timezone != "Asia/Shanghai" || st.EffectiveTimezone != "Asia/Shanghai" || len(st.AllowedOrigins) != 1 {
		t.Fatalf("更新后的设置 = %+v", st)
	}

	// ── errors ──
	_, err = c.GetAgent(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	var me *model.Error
	if !model.IsCode(err, model.CodeNotFound) || !errors.As(err, &me) || me.Message != i18n.ErrorMessage("en", "not_found", nil) {
		t.Fatalf("not_found 错误 = %#v", err)
	}
	if _, err := New(handle.Endpoint, "wrong-token").ListAgents(ctx); !model.IsCode(err, model.CodeUnauthorized) {
		t.Fatalf("错误令牌应返回 unauthorized，实际 %v", err)
	}

	// ── providers ──
	const apiKey = "sk-e2e-SECRET-KEY"
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"e2e-model"}]}`))
	}))
	defer models.Close()
	prov := must[*ProviderView](t, "CreateProvider")(c.CreateProvider(ctx, ProviderCreate{
		Name: "本机", Kind: model.ProviderOpenAICompatible, BaseURL: models.URL + "/v1", APIKey: apiKey, DefaultModel: "e2e-model",
	}))
	if !prov.HasSecret {
		t.Fatal("has_secret 应为 true")
	}
	if list := must[[]ProviderView](t, "ListProviders")(c.ListProviders(ctx)); len(list) != 1 {
		t.Fatalf("供应商列表 = %+v", list)
	}
	must[*ProviderView](t, "GetProvider")(c.GetProvider(ctx, prov.ID))
	if p := must[*ProviderView](t, "UpdateProvider")(c.UpdateProvider(ctx, prov.ID, ProviderPatch{Name: Ptr("本机模型")})); p.Name != "本机模型" || !p.HasSecret {
		t.Fatalf("更新后的供应商 = %+v", p)
	}
	if res := must[*ProviderTestResult](t, "TestProvider")(c.TestProvider(ctx, prov.ID)); !res.OK || len(res.Models) != 1 {
		t.Fatalf("连通性测试 = %+v", res)
	}

	// ── agents ──
	ag := must[*model.Agent](t, "CreateAgent")(c.CreateAgent(ctx, AgentCreate{
		Name: "端到端", Model: model.ModelRef{ProviderID: prov.ID, Model: "e2e-model"},
	}))
	if list := must[[]*model.Agent](t, "ListAgents")(c.ListAgents(ctx)); len(list) != 1 {
		t.Fatalf("智能体列表 = %+v", list)
	}
	must[*model.Agent](t, "GetAgent")(c.GetAgent(ctx, ag.ID))
	must[*model.Agent](t, "UpdateAgent(tools=[])")(c.UpdateAgent(ctx, ag.ID, AgentPatch{Tools: &[]string{}}))
	if specs := must[[]model.ToolSpec](t, "AgentTools")(c.AgentTools(ctx, ag.ID)); len(specs) != 0 {
		t.Fatalf("清空后的工具 = %+v", specs)
	}
	var reset []string
	must[*model.Agent](t, "UpdateAgent(tools=null)")(c.UpdateAgent(ctx, ag.ID, AgentPatch{Tools: &reset}))
	if specs := must[[]model.ToolSpec](t, "AgentTools")(c.AgentTools(ctx, ag.ID)); len(specs) == 0 {
		t.Fatal("重置后应恢复默认工具集")
	}
	if err := c.DeleteProvider(ctx, prov.ID); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("被引用的供应商删除应返回 conflict，实际 %v", err)
	}

	// ── sessions / messages / runs ──
	sess := must[*SessionView](t, "CreateSession")(c.CreateSession(ctx, ag.ID, "端到端会话"))
	if list := must[[]SessionView](t, "ListSessions")(c.ListSessions(ctx, SessionQuery{AgentID: ag.ID, Limit: 10})); len(list) != 1 {
		t.Fatalf("会话列表 = %+v", list)
	}
	// session.* 事件的载荷与 REST 响应同形（session.View）。
	created := log.waitFor(t, "session.created", func(ev event.Event) bool { return ev.Type == "session.created" && ev.SessionID == sess.ID })
	var createdView SessionView
	if err := DecodePayload(created, &createdView); err != nil || createdView.ID != sess.ID || createdView.Origin != model.OriginDesktop {
		t.Fatalf("session.created 载荷 = %+v %v", createdView, err)
	}
	cliSess := must[*SessionView](t, "CreateSessionWith(cli)")(c.CreateSessionWith(ctx, SessionCreate{AgentID: ag.ID, Origin: model.OriginCLI}))
	if cliSess.Origin != model.OriginCLI || cliSess.Key == sess.Key {
		t.Fatalf("CLI 会话 = %+v", cliSess)
	}
	if s := must[*SessionView](t, "UpdateSession(空)")(c.UpdateSession(ctx, cliSess.ID, SessionPatch{})); s.ID != cliSess.ID {
		t.Fatalf("空 PATCH 应返回当前会话，实际 %+v", s)
	}
	mustOK(t, "DeleteSession(cli)", c.DeleteSession(ctx, cliSess.ID))
	run := must[*model.Run](t, "SendMessage")(c.SendMessage(ctx, sess.ID, "hello", nil))
	completed := log.waitFor(t, "run.completed", ofType(string(model.EventRunCompleted), run.ID))
	var re model.RunEvent
	if err := DecodePayload(completed, &re); err != nil || re.Message == nil || re.Message.Text() != "echo: hello" {
		t.Fatalf("run.completed 载荷 = %+v %v", re, err)
	}
	if got := must[*model.Run](t, "GetRun")(c.GetRun(ctx, run.ID)); got.Status != model.RunCompleted {
		t.Fatalf("运行状态 = %s", got.Status)
	}
	msgs := must[[]*model.Message](t, "ListMessages")(c.ListMessages(ctx, sess.ID, 0, 0))
	if len(msgs) != 2 {
		t.Fatalf("消息 = %+v", msgs)
	}
	if after := must[[]*model.Message](t, "ListMessages(after)")(c.ListMessages(ctx, sess.ID, msgs[0].Seq, 10)); len(after) != 1 {
		t.Fatalf("after_seq 结果 = %+v", after)
	}
	if s := must[*SessionView](t, "UpdateSession")(c.UpdateSession(ctx, sess.ID, SessionPatch{Title: Ptr("改名")})); s.Title != "改名" {
		t.Fatalf("重命名结果 = %+v", s)
	}

	att := must[*model.Attachment](t, "UploadAttachment")(c.UploadAttachment(ctx, sess.ID, "说明.txt", strings.NewReader("attachment body")))
	if !strings.HasPrefix(att.Path, "inbound/") || att.Size != int64(len("attachment body")) {
		t.Fatalf("附件 = %+v", att)
	}
	run2 := must[*model.Run](t, "SendMessage(附件)")(c.SendMessage(ctx, sess.ID, "看附件", []model.Attachment{*att}))
	log.waitFor(t, "带附件的运行完成", ofType(string(model.EventRunCompleted), run2.ID))

	held := must[*model.Run](t, "SendMessage(/hold)")(c.SendMessage(ctx, sess.ID, "/hold", nil))
	log.waitFor(t, "run.started", ofType(string(model.EventRunStarted), held.ID))
	if s := must[*SessionView](t, "GetSession")(c.GetSession(ctx, sess.ID)); s.ActiveRunID != held.ID {
		t.Fatalf("active_run_id = %q", s.ActiveRunID)
	}
	if _, err := c.SendMessage(ctx, sess.ID, "again", nil); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("在途运行时再次发送应返回 conflict，实际 %v", err)
	}
	must[*model.Run](t, "CancelRun")(c.CancelRun(ctx, held.ID))
	log.waitFor(t, "run.cancelled", ofType(string(model.EventRunCancelled), held.ID))

	failed := must[*model.Run](t, "SendMessage(/fail)")(c.SendMessage(ctx, sess.ID, "/fail", nil))
	failedEv := log.waitFor(t, "run.failed", ofType(string(model.EventRunFailed), failed.ID))
	if err := DecodePayload(failedEv, &re); err != nil || re.Error != i18n.T("en", "run.error.run_timeout", nil) {
		t.Fatalf("run.failed 事件里的错误应本地化: %+v %v", re, err)
	}
	if got := must[*model.Run](t, "GetRun(failed)")(c.GetRun(ctx, failed.ID)); got.Error != i18n.T("en", "run.error.run_timeout", nil) {
		t.Fatalf("运行的失败原因应本地化，实际 %q", got.Error)
	}

	// ── approvals / audit ──
	appr := must[*model.Run](t, "SendMessage(/approve)")(c.SendMessage(ctx, sess.ID, "/approve", nil))
	log.waitFor(t, "approval.requested", ofType(string(model.EventApprovalRequested), appr.ID))
	pending := must[[]*model.ApprovalRequest](t, "ListApprovals")(c.ListApprovals(ctx, "", 10, 0))
	if len(pending) != 1 || pending[0].RunID != appr.ID {
		t.Fatalf("待审批 = %+v", pending)
	}
	decided := must[*model.ApprovalRequest](t, "DecideApproval")(c.DecideApproval(ctx, pending[0].ID, model.Decision{Approve: true}))
	if decided.Status != model.ApprovalApproved || decided.DecidedBy != identity.LocalPrincipalID {
		t.Fatalf("审批结果 = %+v", decided)
	}
	log.waitFor(t, "批准后运行完成", ofType(string(model.EventRunCompleted), appr.ID))
	audit := must[[]*model.AuditEntry](t, "ListAudit")(c.ListAudit(ctx, AuditQuery{Action: "approval.decide", Since: time.Now().Add(-time.Hour)}))
	if len(audit) == 0 {
		t.Fatal("审计里应有审批决定")
	}

	// ── jobs ──
	job := must[*model.Job](t, "CreateJob")(c.CreateJob(ctx, JobCreate{
		AgentID: ag.ID, Name: "日报", Schedule: "@daily", Kind: model.JobPushText, Input: "早",
	}))
	if !job.Enabled || job.MissedPolicy != model.MissedSkip {
		t.Fatalf("任务缺省值 = %+v", job)
	}
	if list := must[[]*model.Job](t, "ListJobs")(c.ListJobs(ctx, ag.ID)); len(list) != 1 {
		t.Fatalf("任务列表 = %+v", list)
	}
	must[*model.Job](t, "GetJob")(c.GetJob(ctx, job.ID))
	if j := must[*model.Job](t, "UpdateJob")(c.UpdateJob(ctx, job.ID, JobPatch{Enabled: Ptr(false)})); j.Enabled {
		t.Fatal("停用任务失败")
	}
	mustOK(t, "RunJob", c.RunJob(ctx, job.ID))
	log.waitFor(t, "job.triggered", ofType("job.triggered", ""))
	mustOK(t, "DeleteJob", c.DeleteJob(ctx, job.ID))

	// ── extensions ──
	skillDir := filepath.Join(t.TempDir(), "e2e-skill")
	mustOK(t, "创建技能目录", os.MkdirAll(skillDir, 0o700))
	mustOK(t, "写 SKILL.md", os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: e2e-skill\ndescription: 端到端技能\n---\n\n正文\n"), 0o600))
	ext := must[*model.Extension](t, "InstallExtension")(c.InstallExtension(ctx, skillDir))
	if ext.Kind != model.ExtensionSkill {
		t.Fatalf("扩展 = %+v", ext)
	}
	mcpExt := must[*model.Extension](t, "RegisterMCP")(c.RegisterMCP(ctx, "e2e-mcp", model.MCPServerConfig{Transport: model.MCPStdio, Command: "mcp-demo"}))
	if list := must[[]*model.Extension](t, "ListExtensions")(c.ListExtensions(ctx, model.ExtensionSkill)); len(list) != 1 {
		t.Fatalf("技能列表 = %+v", list)
	}
	must[*model.Extension](t, "GetExtension")(c.GetExtension(ctx, ext.ID))
	if e := must[*model.Extension](t, "SetExtensionEnabled(false)")(c.SetExtensionEnabled(ctx, ext.ID, false)); e.Enabled {
		t.Fatal("停用扩展失败")
	}
	must[*model.Extension](t, "SetExtensionEnabled(true)")(c.SetExtensionEnabled(ctx, ext.ID, true))
	if s := must[*ExtensionStatus](t, "ExtensionStatus")(c.ExtensionStatus(ctx, mcpExt.ID)); s.ID != mcpExt.ID || s.Connected {
		t.Fatalf("扩展状态 = %+v", s)
	}
	if s := must[*ExtensionStatus](t, "AcceptExtensionTools")(c.AcceptExtensionTools(ctx, ext.ID)); s.ID != ext.ID {
		t.Fatalf("确认工具后的状态 = %+v", s)
	}
	mustOK(t, "UninstallExtension", c.UninstallExtension(ctx, ext.ID))
	mustOK(t, "UninstallExtension(mcp)", c.UninstallExtension(ctx, mcpExt.ID))

	// ── channels（testdaemon 用生产的 channel.Service：支持的类型与归属校验同生产） ──
	if _, err := c.CreateChannel(ctx, ChannelCreate{AgentID: ag.ID, Kind: model.ChannelFeishu, Name: "f"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("生产网关不支持的通道类型应返回 invalid，实际 %v", err)
	}
	if _, err := c.CreateChannel(ctx, ChannelCreate{AgentID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: model.ChannelConsole, Name: "c"}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的智能体应返回 not_found，实际 %v", err)
	}
	const botToken = "bot:E2E-SECRET"
	ch := must[*ChannelView](t, "CreateChannel")(c.CreateChannel(ctx, ChannelCreate{
		AgentID: ag.ID, Kind: model.ChannelTelegram, Name: "tg", Secret: botToken,
		Config: json.RawMessage(`{"allowed_peers":["1"]}`),
	}))
	if !ch.HasSecret || ch.Status != "running" {
		t.Fatalf("通道 = %+v", ch)
	}
	if list := must[[]ChannelView](t, "ListChannels")(c.ListChannels(ctx, ag.ID)); len(list) != 1 {
		t.Fatalf("通道列表 = %+v", list)
	}
	must[*ChannelView](t, "GetChannel")(c.GetChannel(ctx, ch.ID))
	if u := must[*ChannelView](t, "UpdateChannel")(c.UpdateChannel(ctx, ch.ID, ChannelPatch{Secret: Ptr(""), Enabled: Ptr(false)})); u.HasSecret || u.Status != "stopped" {
		t.Fatalf("更新后的通道 = %+v", u)
	}
	must[*ChannelView](t, "ReloadChannel")(c.ReloadChannel(ctx, ch.ID))
	mustOK(t, "DeleteChannel", c.DeleteChannel(ctx, ch.ID))

	// ── 事件流：断线续传（事件仍在回放缓冲内，不应出现 gap） ──
	before := proxy.accepts()
	proxy.cut(false)
	s2 := must[*SessionView](t, "CreateSession(断线期间)")(c.CreateSession(ctx, ag.ID, "断线后"))
	log.waitFor(t, "断线重连后收到 session.created", func(ev event.Event) bool {
		return ev.Type == "session.created" && ev.SessionID == s2.ID
	})
	if proxy.accepts() <= before {
		t.Fatal("事件流应已重连")
	}

	// ── 事件流：回放缓冲被覆盖 → gap，随后从 gap 的位置继续 ──
	proxy.cut(true)
	for i := 0; i < busCapacity+4; i++ {
		must[*SessionView](t, "CreateSession(制造 gap)")(c.CreateSession(ctx, ag.ID, fmt.Sprintf("填充 %d", i)))
	}
	proxy.cut(false)
	gap := log.waitFor(t, "gap", ofType(EventGap, ""))
	if gap.Seq == 0 {
		t.Fatalf("gap 应携带续传位置: %+v", gap)
	}
	s3 := must[*SessionView](t, "CreateSession(gap 之后)")(c.CreateSession(ctx, ag.ID, "gap 之后"))
	after := log.waitFor(t, "gap 之后的事件", func(ev event.Event) bool {
		return ev.Type == "session.created" && ev.SessionID == s3.ID
	})
	if after.Seq <= gap.Seq {
		t.Fatalf("gap 之后的事件 Seq %d 应大于 gap 的 %d", after.Seq, gap.Seq)
	}

	// ── 清理：删除会话、智能体、供应商 ──
	mustOK(t, "DeleteSession", c.DeleteSession(ctx, s2.ID))
	mustOK(t, "DeleteAgent", c.DeleteAgent(ctx, ag.ID))
	mustOK(t, "DeleteProvider", c.DeleteProvider(ctx, prov.ID))
	if _, err := c.GetSession(ctx, sess.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("删除智能体应级联删除会话，实际 %v", err)
	}
	if strings.Contains(d.stderr.String(), apiKey) || strings.Contains(d.stderr.String(), botToken) {
		t.Fatal("守护进程输出泄露了密钥")
	}

	// ── 经 API 关闭：错误令牌被拒绝；正确令牌 202 后进程退出、daemon.json 被删除 ──
	if err := New(handle.Endpoint, "wrong-token").Shutdown(ctx); !model.IsCode(err, model.CodeUnauthorized) {
		t.Fatalf("错误令牌的关闭请求应返回 unauthorized，实际 %v", err)
	}
	mustOK(t, "Shutdown", c.Shutdown(ctx))
	d.waitExit(t, 15*time.Second)
	if _, err := identity.NewHandleFile(d.dir).Read(ctx); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("关闭后 daemon.json 应被删除，实际 %v", err)
	}
}
