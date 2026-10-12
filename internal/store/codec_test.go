package store

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestTimeFormatIsFixedWidthAndSortable(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	times := []time.Time{
		base.Add(500 * time.Millisecond),
		base,
		base.Add(123 * time.Millisecond),
		base.Add(time.Second),
		base.Add(1),
		time.Date(2026, 1, 2, 11, 4, 5, 0, time.FixedZone("CST", 8*3600)), // 等于 base，非 UTC 表示
	}
	var encoded []string
	for _, tm := range times {
		s := fmtTime(tm)
		if len(s) != len("2026-01-02T03:04:05.000000000Z") {
			t.Errorf("时间文本应定宽: %q", s)
		}
		back, err := parseTime(s)
		if err != nil || !back.Equal(tm) {
			t.Errorf("时间应无损往返: %v → %q → %v (err=%v)", tm, s, back, err)
		}
		encoded = append(encoded, s)
	}
	sort.Strings(encoded)
	for i := 1; i < len(encoded); i++ {
		a, _ := parseTime(encoded[i-1])
		b, _ := parseTime(encoded[i])
		if a.After(b) {
			t.Errorf("字典序应等于时间序: %q 排在 %q 之前", encoded[i-1], encoded[i])
		}
	}
	// 变宽的 RFC 3339 做不到这一点："…05Z" 会排在 "…05.5Z" 之后。
	if fmtTime(base) >= fmtTime(base.Add(500*time.Millisecond)) {
		t.Errorf("整秒应排在同秒的小数之前")
	}
}

func TestNullableTimeScanning(t *testing.T) {
	var nt dbNullTime
	if err := nt.Scan(nil); err != nil || nt.t != nil {
		t.Errorf("NULL 应扫描为 nil: %v %v", nt.t, err)
	}
	if err := nt.Scan("2026-01-02T03:04:05.000000000Z"); err != nil || nt.t == nil {
		t.Errorf("文本应扫描为时间: %v", err)
	}
	var dt dbTime
	if err := dt.Scan("not a time"); err == nil {
		t.Error("非法时间文本应报错")
	}
	if err := dt.Scan(nil); err == nil {
		t.Error("非空时间列遇到 NULL 应报错")
	}
	if fmtTimePtr(nil) != nil {
		t.Error("nil 时间应存 NULL")
	}
	zero := time.Time{}
	if fmtTimePtr(&zero) != nil {
		t.Error("零值时间应存 NULL")
	}
}

func TestStringsAndJSONCodec(t *testing.T) {
	if v, err := stringsArg("f", nil); err != nil || v != nil {
		t.Errorf("nil 切片应存 NULL: %v %v", v, err)
	}
	if v, err := stringsArg("f", []string{}); err != nil || v != "[]" {
		t.Errorf("空切片应存 '[]': %v %v", v, err)
	}
	var ds dbStrings
	if err := ds.Scan(nil); err != nil || ds.v != nil {
		t.Errorf("NULL 应扫描为 nil 切片")
	}
	if err := ds.Scan("[]"); err != nil || ds.v == nil || len(ds.v) != 0 {
		t.Errorf("'[]' 应扫描为空切片，实际 %#v", ds.v)
	}
	if err := ds.Scan(`["a","b"]`); err != nil || len(ds.v) != 2 {
		t.Errorf("数组应正常解析: %v", err)
	}
	if err := ds.Scan(`{"a":1}`); err == nil {
		t.Error("非数组 JSON 应报错")
	}

	if v, err := jsonArg("f", nil); err != nil || v != nil {
		t.Errorf("空 JSON 应存 NULL")
	}
	if v, err := jsonArg("f", json.RawMessage(`{"a":1}`)); err != nil || v != `{"a":1}` {
		t.Errorf("合法 JSON 应原样存储: %v %v", v, err)
	}
	_, err := jsonArg("f", json.RawMessage(`{`))
	assertCode(t, err, model.CodeInvalid)

	var dj dbJSON
	if err := dj.Scan([]byte(`{"a":1}`)); err != nil || string(dj.raw) != `{"a":1}` {
		t.Errorf("字节切片应扫描为 RawMessage")
	}
	var db dbBool
	if err := db.Scan(int64(1)); err != nil || !db.v {
		t.Errorf("1 应扫描为 true")
	}
	if err := db.Scan("yes"); err == nil {
		t.Error("非整数布尔列应报错")
	}
}

func TestPageDefaults(t *testing.T) {
	if l, o := pageOf(model.Page{}); l != defaultPageLimit || o != 0 {
		t.Errorf("零值 Page 应用默认页大小: %d %d", l, o)
	}
	if l, o := pageOf(model.Page{Limit: 5, Offset: -3}); l != 5 || o != 0 {
		t.Errorf("负 Offset 应归零: %d %d", l, o)
	}
}
