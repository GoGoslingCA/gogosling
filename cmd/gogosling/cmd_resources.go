package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/provider"
)

// 环境变量中的凭据：优先于交互输入，避免出现在命令行参数（ps、shell 历史）里。
const (
	envAPIKey        = "GOGOSLING_API_KEY"
	envChannelSecret = "GOGOSLING_CHANNEL_SECRET"
)

// h 返回表头文案。
func (c *cli) h(keys ...string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = c.t("header."+k, nil)
	}
	return out
}

// emit 输出结果：--json 时写 JSON，否则调用 text。
func (c *cli) emit(v any, text func() error) error {
	if c.asJSON {
		return writeJSON(c.out, v)
	}
	return text()
}

// emitTable 输出表格；没有行时输出"（无）"。
func (c *cli) emitTable(tb *table) error {
	if len(tb.rows) == 0 {
		c.println(c.t("empty", nil))
		return nil
	}
	return tb.render(c.out)
}

// done 输出"已创建 / 已删除 / 已更新"一类的确认。
func (c *cli) done(key, kind, id string, v any) error {
	return c.emit(v, func() error {
		c.println(c.t(key, map[string]any{"kind": kind, "id": id}))
		return nil
	})
}

// ---- agent ------------------------------------------------------------------

func (c *cli) cmdAgent(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("agent", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		pos, err := c.parse(flag.NewFlagSet("agent list", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		agents, err := cl.ListAgents(ctx)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(agents), func() error {
			tb := newTable(c.h("id", "name", "model", "autonomy", "enabled")...)
			for _, a := range agents {
				tb.add(a.ID, a.Name, a.Model.ProviderID+"/"+a.Model.Model, string(a.Autonomy), yesNo(a.Enabled))
			}
			return c.emitTable(tb)
		})
	case "show":
		pos, err := c.parse(flag.NewFlagSet("agent show", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<agent>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		a, err := c.resolveAgent(ctx, cl, pos[0])
		if err != nil {
			return err
		}
		return c.emit(a, func() error {
			tb := newTable(c.h("key", "value")...)
			tb.add("id", a.ID)
			tb.add("name", a.Name)
			tb.add("model", a.Model.ProviderID+"/"+a.Model.Model)
			tb.add("autonomy", string(a.Autonomy))
			tb.add("tools", strings.Join(a.Tools, ", "))
			tb.add("extensions", strings.Join(a.Extensions, ", "))
			tb.add("workspace_dir", a.WorkspaceDir)
			tb.add("template", a.Template)
			tb.add("enabled", yesNo(a.Enabled))
			if err := tb.render(c.out); err != nil {
				return err
			}
			if strings.TrimSpace(a.SystemPrompt) != "" {
				c.println("")
				c.println(a.SystemPrompt)
			}
			return nil
		})
	case "create":
		fs := flag.NewFlagSet("agent create", flag.ContinueOnError)
		name := fs.String("name", "", "")
		prov := fs.String("provider", "", "")
		mdl := fs.String("model", "", "")
		tpl := fs.String("template", "", "")
		autonomy := fs.String("autonomy", "", "")
		prompt := fs.String("prompt", "", "")
		desc := fs.String("description", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		for flagName, v := range map[string]string{"--name": *name, "--provider": *prov} {
			if strings.TrimSpace(v) == "" {
				return &usageError{msg: c.t("missing_arg", map[string]any{"arg": flagName})}
			}
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		providerID, defaultModel, err := c.resolveProvider(ctx, cl, *prov)
		if err != nil {
			return err
		}
		modelName := strings.TrimSpace(*mdl)
		if modelName == "" {
			modelName = defaultModel
		}
		if modelName == "" {
			return &usageError{msg: c.t("missing_arg", map[string]any{"arg": "--model"})}
		}
		a, err := cl.CreateAgent(ctx, agentInput{
			Name: *name, Description: *desc, SystemPrompt: *prompt,
			Model:    model.ModelRef{ProviderID: providerID, Model: modelName},
			Autonomy: model.AutonomyLevel(*autonomy), Template: *tpl,
		})
		if err != nil {
			return err
		}
		return c.done("created", "agent", a.ID, a)
	case "delete":
		pos, err := c.parse(flag.NewFlagSet("agent delete", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<agent>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		a, err := c.resolveAgent(ctx, cl, pos[0])
		if err != nil {
			return err
		}
		if err := cl.DeleteAgent(ctx, a.ID); err != nil {
			return err
		}
		return c.done("deleted", "agent", a.ID, map[string]string{"id": a.ID})
	default:
		return c.unknownSub("agent", sub)
	}
}

// resolveAgent 按 ID 或名称（不区分大小写）找到智能体；名称重复时要求改用 ID。
func (c *cli) resolveAgent(ctx context.Context, cl api, ref string) (model.Agent, error) {
	ref = strings.TrimSpace(ref)
	if model.IsID(ref) {
		a, err := cl.GetAgent(ctx, ref)
		if err == nil || !model.IsCode(err, model.CodeNotFound) {
			return a, err
		}
	}
	agents, err := cl.ListAgents(ctx)
	if err != nil {
		return model.Agent{}, err
	}
	var found []model.Agent
	for _, a := range agents {
		if a.ID == ref || strings.EqualFold(a.Name, ref) {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return model.Agent{}, errors.New(c.t("agent_not_found", map[string]any{"agent": ref}))
	default:
		return model.Agent{}, errors.New(c.t("agent_ambiguous", map[string]any{"agent": ref}))
	}
}

// resolveProvider 按 ID 或名称找到供应商，返回 ID 与默认模型。
func (c *cli) resolveProvider(ctx context.Context, cl api, ref string) (string, string, error) {
	providers, err := cl.ListProviders(ctx)
	if err != nil {
		return "", "", err
	}
	for _, p := range providers {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p.ID, p.DefaultModel, nil
		}
	}
	return "", "", &model.Error{Code: model.CodeNotFound, Message: "provider " + ref}
}

// ---- provider ---------------------------------------------------------------

func (c *cli) cmdProvider(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("provider", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		pos, err := c.parse(flag.NewFlagSet("provider list", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		list, err := cl.ListProviders(ctx)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(list), func() error {
			tb := newTable(c.h("id", "name", "kind", "base_url", "default_model", "has_secret", "enabled")...)
			for _, p := range list {
				tb.add(p.ID, p.Name, string(p.Kind), p.BaseURL, p.DefaultModel, yesNo(p.HasSecret), yesNo(p.Enabled))
			}
			return c.emitTable(tb)
		})
	case "presets":
		pos, err := c.parse(flag.NewFlagSet("provider presets", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		presets := provider.Presets()
		return c.emit(itemsOf(presets), func() error {
			tb := newTable(c.h("id", "name", "kind", "base_url", "default_model", "requires_key")...)
			for _, p := range presets {
				tb.add(p.ID, p.Name, string(p.Kind), p.BaseURL, p.DefaultModel, yesNo(p.RequiresAPIKey))
			}
			return c.emitTable(tb)
		})
	case "add":
		return c.providerAdd(ctx, rest)
	case "test":
		pos, err := c.parse(flag.NewFlagSet("provider test", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<provider>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		id, _, err := c.resolveProvider(ctx, cl, pos[0])
		if err != nil {
			return err
		}
		res, err := cl.TestProvider(ctx, id)
		if err != nil {
			return err
		}
		return c.emit(res, func() error {
			if res.OK {
				c.println(c.t("provider_test_ok", map[string]any{"latency": res.LatencyMS, "models": strings.Join(res.Models, ", ")}))
				return nil
			}
			c.println(c.t("provider_test_failed", map[string]any{"error": res.Error}))
			return &silentError{code: exitFailure}
		})
	case "delete":
		pos, err := c.parse(flag.NewFlagSet("provider delete", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<provider>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		id, _, err := c.resolveProvider(ctx, cl, pos[0])
		if err != nil {
			return err
		}
		if err := cl.DeleteProvider(ctx, id); err != nil {
			return err
		}
		return c.done("deleted", "provider", id, map[string]string{"id": id})
	default:
		return c.unknownSub("provider", sub)
	}
}

func (c *cli) providerAdd(ctx context.Context, rest []string) error {
	fs := flag.NewFlagSet("provider add", flag.ContinueOnError)
	name := fs.String("name", "", "")
	kind := fs.String("kind", string(model.ProviderOpenAICompatible), "")
	baseURL := fs.String("base-url", "", "")
	mdl := fs.String("model", "", "")
	apiKey := fs.String("api-key", "", "")
	preset := fs.String("preset", "", "")
	pos, err := c.parse(fs, rest)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	in := providerInput{Name: strings.TrimSpace(*name), Kind: model.ProviderKind(*kind), BaseURL: *baseURL, DefaultModel: *mdl}
	needsKey := true
	if *preset != "" {
		p, ok := provider.PresetByID(*preset)
		if !ok {
			return &usageError{msg: c.t("invalid_arg", map[string]any{"arg": "--preset", "reason": *preset})}
		}
		in.Kind, needsKey = p.Kind, p.RequiresAPIKey
		if in.BaseURL == "" {
			in.BaseURL = p.BaseURL
		}
		if in.DefaultModel == "" {
			in.DefaultModel = p.DefaultModel
		}
		if in.Name == "" {
			in.Name = p.Name
		}
	}
	if in.Name == "" {
		return &usageError{msg: c.t("missing_arg", map[string]any{"arg": "--name"})}
	}
	key := strings.TrimSpace(*apiKey)
	if key == "" {
		key = strings.TrimSpace(c.getenv(envAPIKey))
	}
	if key == "" && needsKey && c.stdinTTY {
		if key, err = c.readSecret(ctx, c.t("api_key_prompt", nil)); err != nil {
			return err
		}
	}
	in.APIKey = key
	cl, err := c.connect(ctx)
	if err != nil {
		return err
	}
	p, err := cl.CreateProvider(ctx, in)
	if err != nil {
		return err
	}
	return c.done("created", "provider", p.ID, p)
}

// ---- session ----------------------------------------------------------------

func (c *cli) cmdSession(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("session", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("session list", flag.ContinueOnError)
		agentRef := fs.String("agent", "", "")
		all := fs.Bool("all", false, "")
		limit := fs.Int("limit", 50, "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		f := sessionFilter{Limit: *limit}
		if !*all {
			f.Status = string(model.SessionActive)
		}
		if *agentRef != "" {
			a, err := c.resolveAgent(ctx, cl, *agentRef)
			if err != nil {
				return err
			}
			f.AgentID = a.ID
		}
		list, err := cl.ListSessions(ctx, f)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(list), func() error {
			tb := newTable(c.h("id", "agent", "title", "origin", "status", "updated")...)
			for _, s := range list {
				status := string(s.Status)
				if s.ActiveRunID != "" {
					status += "*"
				}
				tb.add(s.ID, s.AgentID, s.Title, string(s.Origin), status, formatTime(s.UpdatedAt))
			}
			return c.emitTable(tb)
		})
	case "new":
		fs := flag.NewFlagSet("session new", flag.ContinueOnError)
		title := fs.String("title", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<agent>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		a, err := c.resolveAgent(ctx, cl, pos[0])
		if err != nil {
			return err
		}
		s, err := cl.CreateSession(ctx, a.ID, *title)
		if err != nil {
			return err
		}
		return c.done("created", "session", s.ID, s)
	case "archive", "delete":
		pos, err := c.parse(flag.NewFlagSet("session "+sub, flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<session>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		if sub == "archive" {
			s, err := cl.ArchiveSession(ctx, pos[0])
			if err != nil {
				return err
			}
			return c.done("updated", "session", s.ID, s)
		}
		if err := cl.DeleteSession(ctx, pos[0]); err != nil {
			return err
		}
		return c.done("deleted", "session", pos[0], map[string]string{"id": pos[0]})
	default:
		return c.unknownSub("session", sub)
	}
}

// ---- approvals --------------------------------------------------------------

func (c *cli) cmdApprovals(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("approvals", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("approvals list", flag.ContinueOnError)
		status := fs.String("status", string(model.ApprovalPending), "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		list, err := cl.ListApprovals(ctx, *status)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(list), func() error {
			tb := newTable(c.h("id", "tool", "risk", "summary", "status", "expires")...)
			for _, a := range list {
				tb.add(a.ID, a.ToolCall.Name, string(a.Risk), a.Summary, string(a.Status), formatTime(a.ExpiresAt))
			}
			return c.emitTable(tb)
		})
	case "decide":
		fs := flag.NewFlagSet("approvals decide", flag.ContinueOnError)
		approve := fs.Bool("approve", false, "")
		deny := fs.Bool("deny", false, "")
		scope := fs.String("scope", string(model.ScopeOnce), "")
		reason := fs.String("reason", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<approval>"); err != nil {
			return err
		}
		if *approve == *deny {
			return &usageError{msg: c.t("missing_arg", map[string]any{"arg": "--approve | --deny"})}
		}
		sc := model.ApprovalScope(*scope)
		if sc != model.ScopeOnce && sc != model.ScopeSession && sc != model.ScopeAlways {
			return &usageError{msg: c.t("invalid_arg", map[string]any{"arg": "--scope", "reason": *scope})}
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		res, err := cl.DecideApproval(ctx, pos[0], model.Decision{Approve: *approve, Scope: sc, Reason: *reason})
		if err != nil {
			return err
		}
		return c.emit(res, func() error {
			c.println(c.t("approval_decided", map[string]any{"id": res.ID, "status": string(res.Status)}))
			return nil
		})
	default:
		return c.unknownSub("approvals", sub)
	}
}

// ---- job --------------------------------------------------------------------

func (c *cli) cmdJob(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("job", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("job list", flag.ContinueOnError)
		agentRef := fs.String("agent", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		agentID := ""
		if *agentRef != "" {
			a, err := c.resolveAgent(ctx, cl, *agentRef)
			if err != nil {
				return err
			}
			agentID = a.ID
		}
		jobs, err := cl.ListJobs(ctx, agentID)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(jobs), func() error {
			tb := newTable(c.h("id", "name", "schedule", "kind", "enabled", "next_run", "last_status")...)
			for _, j := range jobs {
				tb.add(j.ID, j.Name, j.Schedule, string(j.Kind), yesNo(j.Enabled), formatTimePtr(j.NextRunAt), string(j.LastStatus))
			}
			return c.emitTable(tb)
		})
	case "create":
		fs := flag.NewFlagSet("job create", flag.ContinueOnError)
		agentRef := fs.String("agent", "", "")
		name := fs.String("name", "", "")
		schedule := fs.String("schedule", "", "")
		kind := fs.String("kind", string(model.JobRunAgent), "")
		input := fs.String("input", "", "")
		tz := fs.String("timezone", "", "")
		key := fs.String("session-key", "", "")
		missed := fs.String("missed", "", "")
		disabled := fs.Bool("disabled", false, "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		for flagName, v := range map[string]string{"--agent": *agentRef, "--name": *name, "--schedule": *schedule, "--input": *input} {
			if strings.TrimSpace(v) == "" {
				return &usageError{msg: c.t("missing_arg", map[string]any{"arg": flagName})}
			}
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		a, err := c.resolveAgent(ctx, cl, *agentRef)
		if err != nil {
			return err
		}
		enabled := !*disabled
		j, err := cl.CreateJob(ctx, jobInput{
			AgentID: a.ID, Name: *name, Schedule: *schedule, Timezone: *tz, Kind: model.JobKind(*kind),
			Input: *input, SessionKey: *key, MissedPolicy: model.MissedPolicy(*missed), Enabled: &enabled,
		})
		if err != nil {
			return err
		}
		return c.done("created", "job", j.ID, j)
	case "delete", "run":
		pos, err := c.parse(flag.NewFlagSet("job "+sub, flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<job>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		if sub == "run" {
			if err := cl.RunJob(ctx, pos[0]); err != nil {
				return err
			}
			return c.emit(map[string]string{"id": pos[0], "status": "triggered"}, func() error {
				c.println(c.t("job_triggered", map[string]any{"id": pos[0]}))
				return nil
			})
		}
		if err := cl.DeleteJob(ctx, pos[0]); err != nil {
			return err
		}
		return c.done("deleted", "job", pos[0], map[string]string{"id": pos[0]})
	default:
		return c.unknownSub("job", sub)
	}
}

// ---- extension --------------------------------------------------------------

// multiFlag 收集可重复的参数（--arg、--env、--header）。
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// keyValues 把 K=V 列表转成 map；格式错误返回参数错误。
func (c *cli) keyValues(flagName string, items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(items))
	for _, kv := range items {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, &usageError{msg: c.t("invalid_arg", map[string]any{"arg": flagName, "reason": "K=V"})}
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

func (c *cli) cmdExtension(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("extension", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("extension list", flag.ContinueOnError)
		kind := fs.String("kind", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		list, err := cl.ListExtensions(ctx, *kind)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(list), func() error {
			tb := newTable(c.h("id", "name", "kind", "version", "enabled", "source")...)
			for _, e := range list {
				tb.add(e.ID, e.Name, string(e.Kind), e.Version, yesNo(e.Enabled), e.Source)
			}
			return c.emitTable(tb)
		})
	case "install":
		pos, err := c.parse(flag.NewFlagSet("extension install", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<path>"); err != nil {
			return err
		}
		// 守护进程与 CLI 的工作目录可能不同：相对路径在这里转成绝对路径。
		path, err := filepath.Abs(pos[0])
		if err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		e, err := cl.InstallExtension(ctx, path)
		if err != nil {
			return err
		}
		return c.done("created", string(e.Kind), e.ID, e)
	case "mcp":
		return c.extensionMCP(ctx, rest)
	case "enable", "disable":
		pos, err := c.parse(flag.NewFlagSet("extension "+sub, flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<extension>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		e, err := cl.SetExtensionEnabled(ctx, pos[0], sub == "enable")
		if err != nil {
			return err
		}
		return c.done("updated", string(e.Kind), e.ID, e)
	case "remove":
		pos, err := c.parse(flag.NewFlagSet("extension remove", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<extension>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		if err := cl.DeleteExtension(ctx, pos[0]); err != nil {
			return err
		}
		return c.done("deleted", "extension", pos[0], map[string]string{"id": pos[0]})
	case "status", "accept-tools":
		// status 查看 MCP 连接状态与工具声明是否变化；accept-tools 在核对变化后重新固定工具声明，
		// 变化的工具随后重新对模型可见（对应 POST /extensions/{id}/accept-tools）。
		pos, err := c.parse(flag.NewFlagSet("extension "+sub, flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<extension>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		if sub == "accept-tools" {
			st, err := cl.AcceptExtensionTools(ctx, pos[0])
			if err != nil {
				return err
			}
			return c.done("updated", "extension", st.ID, st)
		}
		st, err := cl.ExtensionStatus(ctx, pos[0])
		if err != nil {
			return err
		}
		return c.emit(st, func() error {
			tb := newTable(c.h("key", "value")...)
			tb.add("id", st.ID)
			tb.add("connected", yesNo(st.Connected))
			tb.add("tool_count", fmt.Sprint(st.ToolCount))
			tb.add("tools_changed", yesNo(st.ToolsChanged))
			if st.LastError != "" {
				tb.add("last_error", st.LastError)
			}
			return c.emitTable(tb)
		})
	default:
		return c.unknownSub("extension", sub)
	}
}

func (c *cli) extensionMCP(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("extension mcp", args)
	if err != nil {
		return err
	}
	if sub != "add" {
		return c.unknownSub("extension mcp", sub)
	}
	fs := flag.NewFlagSet("extension mcp add", flag.ContinueOnError)
	name := fs.String("name", "", "")
	command := fs.String("command", "", "")
	url := fs.String("url", "", "")
	var argv, env, headers multiFlag
	fs.Var(&argv, "arg", "")
	fs.Var(&env, "env", "")
	fs.Var(&headers, "header", "")
	pos, err := c.parse(fs, rest)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return &usageError{msg: c.t("missing_arg", map[string]any{"arg": "--name"})}
	}
	if (*command == "") == (*url == "") {
		return &usageError{msg: c.t("missing_arg", map[string]any{"arg": "--command | --url"})}
	}
	envMap, err := c.keyValues("--env", env)
	if err != nil {
		return err
	}
	headerMap, err := c.keyValues("--header", headers)
	if err != nil {
		return err
	}
	cfg := model.MCPServerConfig{Transport: model.MCPStdio, Command: *command, Args: argv, Env: envMap}
	if *url != "" {
		cfg = model.MCPServerConfig{Transport: model.MCPHTTP, URL: *url, Headers: headerMap}
	}
	cl, err := c.connect(ctx)
	if err != nil {
		return err
	}
	e, err := cl.RegisterMCP(ctx, *name, cfg)
	if err != nil {
		return err
	}
	return c.done("created", string(e.Kind), e.ID, e)
}

// ---- channel ----------------------------------------------------------------

func (c *cli) cmdChannel(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("channel", args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("channel list", flag.ContinueOnError)
		agentRef := fs.String("agent", "", "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		agentID := ""
		if *agentRef != "" {
			a, err := c.resolveAgent(ctx, cl, *agentRef)
			if err != nil {
				return err
			}
			agentID = a.ID
		}
		list, err := cl.ListChannels(ctx, agentID)
		if err != nil {
			return err
		}
		return c.emit(itemsOf(list), func() error {
			tb := newTable(c.h("id", "name", "kind", "agent", "enabled", "has_secret", "status", "last_error")...)
			for _, ch := range list {
				tb.add(ch.ID, ch.Name, string(ch.Kind), ch.AgentID, yesNo(ch.Enabled), yesNo(ch.HasSecret), ch.Status, ch.LastError)
			}
			return c.emitTable(tb)
		})
	case "add":
		fs := flag.NewFlagSet("channel add", flag.ContinueOnError)
		agentRef := fs.String("agent", "", "")
		kind := fs.String("kind", "", "")
		name := fs.String("name", "", "")
		cfg := fs.String("config", "", "")
		disabled := fs.Bool("disabled", false, "")
		pos, err := c.parse(fs, rest)
		if err != nil {
			return err
		}
		if err := c.need(pos); err != nil {
			return err
		}
		for flagName, v := range map[string]string{"--agent": *agentRef, "--kind": *kind, "--name": *name} {
			if strings.TrimSpace(v) == "" {
				return &usageError{msg: c.t("missing_arg", map[string]any{"arg": flagName})}
			}
		}
		var raw json.RawMessage
		if strings.TrimSpace(*cfg) != "" {
			if !json.Valid([]byte(*cfg)) {
				return &usageError{msg: c.t("invalid_arg", map[string]any{"arg": "--config", "reason": "JSON"})}
			}
			raw = json.RawMessage(*cfg)
		}
		secret := strings.TrimSpace(c.getenv(envChannelSecret))
		if secret == "" && model.ChannelKind(*kind) != model.ChannelConsole && c.stdinTTY {
			if secret, err = c.readSecret(ctx, c.t("secret_prompt", nil)); err != nil {
				return err
			}
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		a, err := c.resolveAgent(ctx, cl, *agentRef)
		if err != nil {
			return err
		}
		enabled := !*disabled
		ch, err := cl.CreateChannel(ctx, channelInput{
			AgentID: a.ID, Kind: model.ChannelKind(*kind), Name: *name, Secret: secret, Config: raw, Enabled: &enabled,
		})
		if err != nil {
			return err
		}
		return c.done("created", "channel", ch.ID, ch)
	case "remove", "reload":
		pos, err := c.parse(flag.NewFlagSet("channel "+sub, flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<channel>"); err != nil {
			return err
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		if sub == "reload" {
			ch, err := cl.ReloadChannel(ctx, pos[0])
			if err != nil {
				return err
			}
			return c.done("updated", "channel", ch.ID, ch)
		}
		if err := cl.DeleteChannel(ctx, pos[0]); err != nil {
			return err
		}
		return c.done("deleted", "channel", pos[0], map[string]string{"id": pos[0]})
	default:
		return c.unknownSub("channel", sub)
	}
}

// ---- settings ---------------------------------------------------------------

func (c *cli) cmdSettings(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("settings", args)
	if err != nil {
		return err
	}
	switch sub {
	case "get":
		pos, err := c.parse(flag.NewFlagSet("settings get", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if len(pos) > 1 {
			return c.need(pos, "[key]")
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		s, err := cl.GetSettings(ctx)
		if err != nil {
			return err
		}
		if len(pos) == 1 {
			v, ok := s[pos[0]]
			if !ok {
				return &model.Error{Code: model.CodeNotFound, Message: "setting " + pos[0]}
			}
			return c.emit(v, func() error {
				c.println(settingText(v))
				return nil
			})
		}
		return c.emit(s, func() error {
			keys := make([]string, 0, len(s))
			for k := range s {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			tb := newTable(c.h("key", "value")...)
			for _, k := range keys {
				tb.add(k, settingText(s[k]))
			}
			return c.emitTable(tb)
		})
	case "set":
		pos, err := c.parse(flag.NewFlagSet("settings set", flag.ContinueOnError), rest)
		if err != nil {
			return err
		}
		if err := c.need(pos, "<key>", "<value>"); err != nil {
			return err
		}
		key, raw := pos[0], pos[1]
		// 值是合法 JSON（数组、布尔等）时按 JSON 发送，否则按字符串；allowed_origins 支持逗号分隔。
		var v any = raw
		if key == "allowed_origins" && !strings.HasPrefix(strings.TrimSpace(raw), "[") {
			v = splitComma(raw)
		} else if json.Valid([]byte(raw)) && !isBareString(raw) {
			_ = json.Unmarshal([]byte(raw), &v)
		}
		cl, err := c.connect(ctx)
		if err != nil {
			return err
		}
		s, err := cl.UpdateSettings(ctx, map[string]any{key: v})
		if err != nil {
			return err
		}
		return c.emit(s, func() error {
			c.println(c.t("settings_set", map[string]any{"key": key}))
			return nil
		})
	default:
		return c.unknownSub("settings", sub)
	}
}

// settingText 把设置值转成一行文本。
func settingText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = fmt.Sprint(p)
		}
		return strings.Join(parts, ", ")
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}

// splitComma 按逗号切分并去掉空项。
func splitComma(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isBareString 判断 raw 是不是 JSON 数字之外的普通字词（如 zh、Asia/Shanghai 不是合法 JSON，
// 但 "true"、"12" 是）；这里只把以引号、方括号、花括号开头或布尔/null 的值当 JSON。
func isBareString(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	switch raw[0] {
	case '"', '[', '{':
		return false
	}
	return raw != "true" && raw != "false" && raw != "null"
}
