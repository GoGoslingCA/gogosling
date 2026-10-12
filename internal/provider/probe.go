package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// ProbeTimeout 是连通性测试的总超时。
	ProbeTimeout = 10 * time.Second
	// DiscoverTimeout 是本机服务发现的超时：本机不可达应当很快失败。
	DiscoverTimeout = 3 * time.Second
	// DefaultOllamaURL 是 Ollama 的默认本机地址。
	DefaultOllamaURL = "http://127.0.0.1:11434"

	// anthropicVersion 是 Anthropic 接口要求的版本头。
	anthropicVersion = "2023-06-01"
	// maxProbeBody 限制读取的响应体大小，防止异常服务拖垮内存。
	maxProbeBody = 4 << 20
	// maxErrorExcerpt 限制错误信息中引用的响应体长度（字符数）。
	maxErrorExcerpt = 200
)

// TestResult 是一次连通性测试的结果；探测失败不是 Go 错误，而是 OK=false 加 Error 文本。
type TestResult struct {
	OK        bool     `json:"ok"`
	Models    []string `json:"models,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
}

// Discovery 是本机模型服务的探测结果。
type Discovery struct {
	Found   bool               `json:"found"`
	Kind    model.ProviderKind `json:"kind"`
	BaseURL string             `json:"base_url"`
	Models  []string           `json:"models,omitempty"`
}

// Test 对已保存的供应商做连通性测试：请求模型列表接口（不消耗推理额度），
// 成功时把模型名写回记录的 Models 并发布 provider.updated。
// 返回的 error 只表示读取记录或写回失败；探测本身的失败体现在 TestResult 中。
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	p, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return TestResult{}, fmt.Errorf("provider: 读取供应商: %w", err)
	}
	apiKey := ""
	if p.SecretRef != "" {
		apiKey, err = s.secrets.Get(ctx, p.SecretRef)
		if err != nil && !model.IsCode(err, model.CodeNotFound) {
			return TestResult{}, fmt.Errorf("provider: 读取密钥: %w", err)
		}
	}
	res := s.probe(ctx, p.Kind, p.BaseURL, apiKey, ProbeTimeout)
	if !res.OK {
		if res.Error != "" {
			res.Error = p.Name + "：" + res.Error
		}
		s.log.Info("供应商连通性测试失败", "provider_id", id, "kind", string(p.Kind), "error", res.Error)
		return res, nil
	}
	p.Models = res.Models
	p.UpdatedAt = s.now().UTC()
	if err := s.repo.UpdateProvider(ctx, p); err != nil {
		return res, fmt.Errorf("provider: 写回模型列表: %w", err)
	}
	s.publish(EventUpdated, p)
	return res, nil
}

// Probe 对任意（可能尚未保存的）配置做连通性测试，用于表单里的"先测试再保存"。
func (s *Service) Probe(ctx context.Context, kind model.ProviderKind, baseURL, apiKey string) TestResult {
	return s.probe(ctx, kind, baseURL, apiKey, ProbeTimeout)
}

// DiscoverLocal 探测本机 Ollama（默认 http://127.0.0.1:11434）。
// 未发现不是错误：Found 为 false；只有 ctx 已结束时才返回 error。
func (s *Service) DiscoverLocal(ctx context.Context) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, model.Wrap(model.CodeCancelled, "操作未完成", err)
	}
	d := Discovery{Kind: model.ProviderOllama, BaseURL: s.localOllamaURL}
	res := s.probe(ctx, model.ProviderOllama, s.localOllamaURL, "", DiscoverTimeout)
	if !res.OK {
		s.log.Debug("未发现本机 Ollama", "base_url", s.localOllamaURL, "error", res.Error)
		return d, nil
	}
	d.Found = true
	d.Models = res.Models
	return d, nil
}

// probe 执行一次模型列表请求并解析。错误信息中会把密钥替换为 ***。
func (s *Service) probe(ctx context.Context, kind model.ProviderKind, baseURL, apiKey string, timeout time.Duration) TestResult {
	if err := validateKind(kind); err != nil {
		return TestResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL(kind)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint(kind, base), nil)
	if err != nil {
		return TestResult{Error: "服务地址不合法: " + redact(err.Error(), apiKey)}
	}
	req.Header.Set("Accept", "application/json")
	switch kind {
	case model.ProviderAnthropic:
		req.Header.Set("anthropic-version", anthropicVersion)
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
	default:
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return TestResult{LatencyMS: time.Since(start).Milliseconds(), Error: describeTransportError(err, apiKey)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{LatencyMS: latency, Error: "读取响应失败: " + redact(err.Error(), apiKey)}
	}
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if excerpt := excerptBody(redact(string(body), apiKey)); excerpt != "" {
			msg += ": " + excerpt
		}
		return TestResult{LatencyMS: latency, Error: msg}
	}
	models, err := parseModels(kind, body)
	if err != nil {
		return TestResult{LatencyMS: latency, Error: "响应不是预期的模型列表格式: " + err.Error()}
	}
	return TestResult{OK: true, Models: models, LatencyMS: latency}
}

// modelsEndpoint 返回各类型的模型列表地址。
func modelsEndpoint(kind model.ProviderKind, base string) string {
	switch kind {
	case model.ProviderOllama:
		return base + "/api/tags"
	case model.ProviderAnthropic:
		if strings.HasSuffix(base, "/v1") {
			return base + "/models"
		}
		return base + "/v1/models"
	default:
		return base + "/models"
	}
}

// parseModels 解析模型列表：OpenAI 与 Anthropic 为 {"data":[{"id"}]}，Ollama 为 {"models":[{"name"}]}。
// 结果去重并排序，便于界面稳定展示。
func parseModels(kind model.ProviderKind, body []byte) ([]string, error) {
	var names []string
	switch kind {
	case model.ProviderOllama:
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if payload.Models == nil {
			return nil, errors.New("缺少 models 字段")
		}
		for _, m := range payload.Models {
			names = append(names, m.Name)
		}
	default:
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if payload.Data == nil {
			return nil, errors.New("缺少 data 字段")
		}
		for _, m := range payload.Data {
			names = append(names, m.ID)
		}
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// describeTransportError 把网络层错误转成简短说明。
func describeTransportError(err error, apiKey string) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时"
	case errors.Is(err, context.Canceled):
		return "请求被取消"
	}
	return "请求失败: " + redact(err.Error(), apiKey)
}

// redact 把文本中出现的密钥替换掉，保证错误信息可以安全展示与记录。
func redact(text, apiKey string) string {
	if apiKey == "" {
		return text
	}
	return strings.ReplaceAll(text, apiKey, "***")
}

// excerptBody 压缩空白并截断响应体，用于错误提示。
func excerptBody(body string) string {
	text := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(text) <= maxErrorExcerpt {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxErrorExcerpt]) + "…"
}
