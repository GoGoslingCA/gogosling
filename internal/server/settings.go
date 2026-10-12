package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
)

// 设置项在 SettingsRepo 中的键；值为 JSON 文本（字符串值带引号）。
const (
	SettingTimezone       = "timezone"
	SettingLocale         = "locale"
	SettingAllowedOrigins = "allowed_origins"
)

// maxAllowedOrigins 限制设置项里额外 Origin 的数量，避免配置膨胀成任意列表。
const maxAllowedOrigins = 32

// settingsState 是设置项的内存副本：启动时从 Store 读一次，PATCH 成功后整体替换。
// 读多写少，用读写锁保护；writeMu 串行化"读-改-写"，防止并发 PATCH 丢失更新。
type settingsState struct {
	mu       sync.RWMutex
	timezone string
	// loc 是显式保存的语言；空表示未设置（解析时让 Accept-Language 生效）。
	loc     string
	origins []string

	writeMu sync.Mutex
}

// settingsSnapshot 是某一时刻的设置值。
type settingsSnapshot struct {
	timezone string
	locale   string
	origins  []string
}

func (st *settingsState) snapshot() settingsSnapshot {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return settingsSnapshot{timezone: st.timezone, locale: st.loc, origins: slices.Clone(st.origins)}
}

func (st *settingsState) replace(s settingsSnapshot) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.timezone, st.loc, st.origins = s.timezone, s.locale, slices.Clone(s.origins)
}

func (st *settingsState) locale() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.loc
}

// originAllowed 判断已规范化的 Origin 是否在设置项白名单中。
func (st *settingsState) originAllowed(norm string) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return slices.Contains(st.origins, norm)
}

// loadSettings 从 Store 读取设置项。缺失的键取缺省值；格式损坏的值记警告后忽略
// （不让一个坏值阻止守护进程启动）；Store 本身出错返回 CodeUnavailable。
func loadSettings(ctx context.Context, store model.SettingsRepo, log *slog.Logger) (*settingsState, error) {
	st := &settingsState{}
	if tz, ok, err := readStringSetting(ctx, store, SettingTimezone); err != nil {
		return nil, err
	} else if ok {
		if _, lerr := scheduler.LoadLocation(tz); lerr != nil {
			log.Warn("忽略无法识别的时区设置", "value", tz)
		} else {
			st.timezone = tz
		}
	}
	if loc, ok, err := readStringSetting(ctx, store, SettingLocale); err != nil {
		return nil, err
	} else if ok && loc != "" {
		if n, valid := i18n.Normalize(loc); valid {
			st.loc = n
		} else {
			log.Warn("忽略不受支持的语言设置", "value", loc)
		}
	}
	raw, err := store.GetSetting(ctx, SettingAllowedOrigins)
	switch {
	case model.IsCode(err, model.CodeNotFound):
	case err != nil:
		return nil, model.Wrap(model.CodeUnavailable, "读取设置项失败", err)
	default:
		var list []string
		if jerr := json.Unmarshal([]byte(raw), &list); jerr != nil {
			log.Warn("忽略格式损坏的 allowed_origins 设置", "error", jerr)
			break
		}
		for _, o := range list {
			n, nerr := normalizeOrigin(o)
			if nerr != nil {
				log.Warn("忽略非法的 Origin 设置", "origin", o)
				continue
			}
			if !slices.Contains(st.origins, n) {
				st.origins = append(st.origins, n)
			}
		}
	}
	return st, nil
}

// readStringSetting 读取字符串设置：值应为 JSON 字符串，兼容未加引号的旧写法。
func readStringSetting(ctx context.Context, store model.SettingsRepo, key string) (string, bool, error) {
	raw, err := store.GetSetting(ctx, key)
	if model.IsCode(err, model.CodeNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, model.Wrap(model.CodeUnavailable, "读取设置项失败", err)
	}
	var v string
	if json.Unmarshal([]byte(raw), &v) != nil {
		v = raw
	}
	return strings.TrimSpace(v), true, nil
}

// settingsView 是 GET / PATCH /api/v1/settings 的响应（OpenAPI Settings）。
//
// timezone / locale 只反映用户显式保存的值，未设置时省略：客户端据此区分"用户偏好"与"缺省"，
// 未设置语言时按浏览器 / 系统语言选择界面语言。effective_* 是只读的实际生效值。
type settingsView struct {
	Timezone          string   `json:"timezone,omitempty"`
	Locale            string   `json:"locale,omitempty"`
	EffectiveLocale   string   `json:"effective_locale"`
	EffectiveTimezone string   `json:"effective_timezone,omitempty"`
	AllowedOrigins    []string `json:"allowed_origins"`
}

// viewOf 构造设置响应。effective_locale：设置项 > Options.DefaultLocale > i18n.DefaultLocale；
// effective_timezone：设置项 > Options.DefaultTimezone（两者都为空时省略）。
func (s *Server) viewOf(snap settingsSnapshot) settingsView {
	v := settingsView{
		Timezone:          snap.timezone,
		Locale:            snap.locale,
		EffectiveLocale:   s.fallbackLocale(),
		EffectiveTimezone: strings.TrimSpace(s.opts.DefaultTimezone),
		AllowedOrigins:    snap.origins,
	}
	if snap.locale != "" {
		v.EffectiveLocale = snap.locale
	}
	if snap.timezone != "" {
		v.EffectiveTimezone = snap.timezone
	}
	if v.AllowedOrigins == nil {
		v.AllowedOrigins = []string{}
	}
	return v
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, s.viewOf(s.settings.snapshot()))
	return nil
}

// settingsPatch 是 PATCH 请求体；缺席或 null 的字段不修改。
type settingsPatch struct {
	Timezone       *string   `json:"timezone"`
	Locale         *string   `json:"locale"`
	AllowedOrigins *[]string `json:"allowed_origins"`
}

// handleUpdateSettings 校验全部字段后在一个事务里写入，成功后热更新内存副本
// （Origin 白名单与语言设置立即对后续请求生效）。字段给出空串表示清除该设置。
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) error {
	var p settingsPatch
	if err := decodeJSON(r, &p); err != nil {
		return err
	}
	s.settings.writeMu.Lock()
	defer s.settings.writeMu.Unlock()

	next := s.settings.snapshot()
	writes := map[string]string{}
	if p.Timezone != nil {
		tz := strings.TrimSpace(*p.Timezone)
		if tz != "" {
			if _, err := scheduler.LoadLocation(tz); err != nil {
				return err
			}
		}
		next.timezone = tz
		writes[SettingTimezone] = mustJSON(tz)
	}
	if p.Locale != nil {
		loc := strings.TrimSpace(*p.Locale)
		if loc != "" {
			n, ok := i18n.Normalize(loc)
			if !ok {
				return badValue("locale", loc)
			}
			loc = n
		}
		next.locale = loc
		writes[SettingLocale] = mustJSON(loc)
	}
	if p.AllowedOrigins != nil {
		list, err := normalizeOriginList(*p.AllowedOrigins)
		if err != nil {
			return err
		}
		next.origins = list
		writes[SettingAllowedOrigins] = mustJSON(list)
	}

	if len(writes) > 0 {
		keys := make([]string, 0, len(writes))
		for k := range writes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ctx := r.Context()
		err := s.deps.Store.Tx(ctx, func(tx model.Store) error {
			for _, k := range keys {
				if err := tx.SetSetting(ctx, k, writes[k]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.settings.replace(next)
		s.log.Info("设置项已更新", "keys", keys)
	}
	writeJSON(w, http.StatusOK, s.viewOf(next))
	return nil
}

// normalizeOriginList 校验并规范化额外 Origin 列表（去重、保持顺序）。
func normalizeOriginList(in []string) ([]string, error) {
	if len(in) > maxAllowedOrigins {
		return nil, invalid("too_many_origins", "额外 Origin 太多", map[string]any{"max": maxAllowedOrigins})
	}
	out := make([]string, 0, len(in))
	for i, raw := range in {
		n, err := normalizeOrigin(raw)
		if err != nil {
			e := invalid("invalid_origin", "Origin 必须是 http(s)://host[:port]", map[string]any{"index": i, "origin": raw})
			e.Err = err
			return nil, e
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// mustJSON 只用于 string 与 []string，它们的 JSON 编码不会失败。
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
