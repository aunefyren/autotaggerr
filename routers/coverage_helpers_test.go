package routers

import (
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// The helpers here make a handler's database failure reproducible, which is the only
// way to reach the 500 branches every handler carries. Two shapes, because a handler
// usually reads before it writes: dropping a table fails every statement against it,
// while a trigger fails one kind of write and leaves the read in front of it working.

// tableOf resolves a model's table name the way gorm will, so a test names the model
// rather than repeating a string the naming strategy owns.
func tableOf(t *testing.T, db *gorm.DB, model any) string {
	t.Helper()
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		t.Fatalf("parse model %T: %v", model, err)
	}
	return stmt.Schema.Table
}

// dropTable removes a model's table, so every query against it fails.
func dropTable(t *testing.T, db *gorm.DB, model any) {
	t.Helper()
	if err := db.Migrator().DropTable(model); err != nil {
		t.Fatalf("drop table for %T: %v", model, err)
	}
}

// failWrites makes every statement of one kind ("INSERT", "UPDATE" or "DELETE")
// against a model's table abort, while reads keep working.
func failWrites(t *testing.T, db *gorm.DB, model any, op string) {
	t.Helper()
	table := tableOf(t, db, model)
	name := fmt.Sprintf("fail_%s_%s", table, strings.ToLower(op))
	sql := fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'forced failure'); END", name, op, table)
	if err := db.Exec(sql).Error; err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// expectStatus asserts a response code and, when given, a substring of the body.
func expectStatus(t *testing.T, label string, got int, body string, want int, contains string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d: %s", label, got, want, body)
		return
	}
	if contains != "" && !strings.Contains(body, contains) {
		t.Errorf("%s body = %s, want it to mention %q", label, body, contains)
	}
}
