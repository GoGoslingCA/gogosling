package store

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件定义 Go 值与 SQLite 列之间的编码规则，所有仓储共用：
//   - 时间：RFC 3339 UTC 文本，固定 9 位小数（见 timeLayout）；可空时间为 NULL。
//   - JSON：json.RawMessage 原样存文本；长度为 0 存 NULL；写入前校验是否为合法 JSON。
//   - 字符串切片：JSON 数组；nil 存 NULL（与空切片区分）。
//   - 布尔：INTEGER 0/1。

// timeLayout 是时间列的存储格式。固定宽度保证字典序等于时间序。
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// fmtTime 把时间转为 UTC 并按 timeLayout 格式化。
func fmtTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// fmtTimePtr 把可空时间转为列值：nil 或零值存 NULL。
func fmtTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return fmtTime(*t)
}

// parseTime 解析 timeLayout（也兼容任意精度的 RFC 3339）。
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: 时间列格式不合法 %q: %w", s, err)
	}
	return t.UTC(), nil
}

// dbTime 扫描非空时间列。
type dbTime struct{ t time.Time }

// Scan 实现 sql.Scanner。
func (d *dbTime) Scan(src any) error {
	s, err := textOf(src)
	if err != nil {
		return err
	}
	if s == "" {
		return fmt.Errorf("store: 时间列不能为空")
	}
	d.t, err = parseTime(s)
	return err
}

// dbNullTime 扫描可空时间列。
type dbNullTime struct{ t *time.Time }

// Scan 实现 sql.Scanner。
func (d *dbNullTime) Scan(src any) error {
	if src == nil {
		d.t = nil
		return nil
	}
	s, err := textOf(src)
	if err != nil {
		return err
	}
	if s == "" {
		d.t = nil
		return nil
	}
	t, err := parseTime(s)
	if err != nil {
		return err
	}
	d.t = &t
	return nil
}

// dbJSON 扫描可空 JSON 列为 json.RawMessage（NULL → nil）。
type dbJSON struct{ raw json.RawMessage }

// Scan 实现 sql.Scanner。
func (d *dbJSON) Scan(src any) error {
	if src == nil {
		d.raw = nil
		return nil
	}
	s, err := textOf(src)
	if err != nil {
		return err
	}
	if s == "" {
		d.raw = nil
		return nil
	}
	// 复制一份：驱动返回的字节切片在下一次 Scan 后可能被复用。
	d.raw = json.RawMessage([]byte(s))
	return nil
}

// dbStrings 扫描 JSON 数组列为 []string（NULL → nil）。
type dbStrings struct{ v []string }

// Scan 实现 sql.Scanner。
func (d *dbStrings) Scan(src any) error {
	if src == nil {
		d.v = nil
		return nil
	}
	s, err := textOf(src)
	if err != nil {
		return err
	}
	if s == "" {
		d.v = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return fmt.Errorf("store: 字符串数组列不是合法 JSON: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	d.v = out
	return nil
}

// dbBool 扫描 INTEGER 0/1。
type dbBool struct{ v bool }

// Scan 实现 sql.Scanner。
func (d *dbBool) Scan(src any) error {
	switch x := src.(type) {
	case nil:
		d.v = false
	case int64:
		d.v = x != 0
	case bool:
		d.v = x
	default:
		return fmt.Errorf("store: 布尔列类型不支持: %T", src)
	}
	return nil
}

// textOf 把驱动给出的文本型值统一成 string。
func textOf(src any) (string, error) {
	switch x := src.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	default:
		return "", fmt.Errorf("store: 期望文本列，得到 %T", src)
	}
}

// boolInt 把布尔转为 0/1。
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// jsonArg 把 json.RawMessage 转为列值：空 → NULL；非法 JSON → CodeInvalid。
func jsonArg(field string, raw json.RawMessage) (driver.Value, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, model.Errorf(model.CodeInvalid, "store: 字段 %s 不是合法 JSON", field)
	}
	return string(raw), nil
}

// marshalArg 把任意值序列化为 JSON 文本列值。
func marshalArg(field string, v any) (driver.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, model.Wrap(model.CodeInvalid, "store: 字段 "+field+" 无法序列化为 JSON", err)
	}
	return string(b), nil
}

// stringsArg 把 []string 转为列值：nil → NULL，空切片 → '[]'。
func stringsArg(field string, v []string) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return marshalArg(field, v)
}

// unmarshalJSON 解析 JSON 列到目标结构。
func unmarshalJSON(field string, raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("store: 字段 %s 的 JSON 无法解析: %w", field, err)
	}
	return nil
}

// scanner 是 *sql.Row 与 *sql.Rows 的公共子集。
type scanner interface {
	Scan(dest ...any) error
}

// collect 遍历 rows 并用 scan 逐行构造结果。
func collect[T any](rows *sql.Rows, scan func(scanner) (*T, error)) ([]*T, error) {
	defer rows.Close()
	out := []*T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
