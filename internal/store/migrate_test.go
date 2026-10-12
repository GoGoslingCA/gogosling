package store

import (
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestParseMigrationName(t *testing.T) {
	cases := []struct {
		in      string
		version int
		name    string
		ok      bool
	}{
		{"001_init.sql", 1, "init", true},
		{"042_add_thing.sql", 42, "add_thing", true},
		{"000_zero.sql", 0, "", false},
		{"1_init.sql", 0, "", false},
		{"001_Init.sql", 0, "", false},
		{"001_init.pg.sql", 0, "", false},
		{"001-init.sql", 0, "", false},
		{"README.md", 0, "", false},
	}
	for _, c := range cases {
		v, name, ok := parseMigrationName(c.in)
		if ok != c.ok || v != c.version || name != c.name {
			t.Errorf("%s: 期望 (%d, %q, %v)，实际 (%d, %q, %v)", c.in, c.version, c.name, c.ok, v, name, ok)
		}
	}
}

func TestValidateMigrationsRejectsGapsAndDuplicates(t *testing.T) {
	ok, err := validateMigrations([]migration{{version: 2, name: "b"}, {version: 1, name: "a"}})
	if err != nil || len(ok) != 2 || ok[0].version != 1 || ok[1].version != 2 {
		t.Fatalf("连续版本应通过并排序: %v %+v", err, ok)
	}
	_, err = validateMigrations([]migration{{version: 1, name: "a"}, {version: 3, name: "c"}})
	assertCode(t, err, model.CodeInternal)
	_, err = validateMigrations([]migration{{version: 1, name: "a"}, {version: 1, name: "a2"}})
	assertCode(t, err, model.CodeInternal)
	_, err = validateMigrations([]migration{{version: 2, name: "b"}})
	assertCode(t, err, model.CodeInternal)
}

func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("嵌入的迁移应能加载: %v", err)
	}
	if len(ms) == 0 || ms[0].version != 1 || ms[0].name != "init" {
		t.Fatalf("第一份迁移应是 001_init.sql，实际 %+v", ms)
	}
	for _, m := range ms {
		if m.sql == "" {
			t.Errorf("迁移 %03d_%s 不应为空", m.version, m.name)
		}
	}
}
