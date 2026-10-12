package model

import (
	"sort"
	"testing"
	"time"
)

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if !IsID(id) {
		t.Fatalf("NewID() = %q，不是合法 ULID", id)
	}
}

func TestNewIDIsTimeOrdered(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		ids = append(ids, newIDAt(base.Add(time.Duration(i)*time.Millisecond)))
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("按毫秒递增生成的 ID 没有按字典序递增")
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if _, dup := seen[id]; dup {
			t.Fatalf("生成了重复 ID: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestIsIDRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"", "abc", "01ARZ3NDEKTSV4RRFFQ69G5FAV1", "01ARZ3NDEKTSV4RRFFQ69G5FAI", "8ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if IsID(bad) {
			t.Errorf("IsID(%q) = true，期望 false", bad)
		}
	}
}
