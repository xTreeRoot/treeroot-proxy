package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeProductionCopyMigration(t *testing.T) {
	source := os.Getenv("TREEROOT_HOME_MIGRATION_SOURCE")
	if source == "" {
		t.Skip("opt-in production-copy migration")
	}
	cfg, e := readConfig(source)
	if e != nil {
		t.Fatal("source config unavailable")
	}
	live, e := openDB(cfg, "ro", 1)
	if e != nil {
		t.Fatal("read-only source unavailable")
	}
	defer live.Close()
	dir := t.TempDir()
	if e = os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "panel.sqlite3")
	if _, e = live.Exec("VACUUM INTO ?", path); e != nil {
		t.Fatal("consistent copy failed")
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	cfg.DataDir = dir
	db, e := openDB(cfg, "rw", 1)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	snapshot := func() map[string][32]byte {
		result := map[string][32]byte{}
		for _, table := range []string{"users", "admin", "sessions", "user_archives", "domains", "traffic", "egress_devices", "egress_totals", "limit_usage", "limit_daily", "user_limits", "counters", "state"} {
			rows, e := query(context.Background(), db, "SELECT * FROM "+table+" ORDER BY rowid")
			if e != nil {
				t.Fatalf("cannot fingerprint %s", table)
			}
			raw, _ := json.Marshal(rows)
			result[table] = sha256.Sum256(raw)
		}
		return result
	}
	before := snapshot()
	for range 2 {
		if e = migrate(db); e != nil {
			t.Fatal("candidate migration failed")
		}
		for table, hash := range snapshot() {
			if hash != before[table] {
				t.Fatalf("existing %s changed", table)
			}
		}
		var check string
		if e = db.QueryRow("PRAGMA integrity_check").Scan(&check); e != nil || check != "ok" {
			t.Fatal("copy integrity failed")
		}
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM home_gateways").Scan(&n); e != nil || n != 0 {
			t.Fatal("migration generated gateway data")
		}
	}
	t.Log(fmt.Sprintf("Compatible migration repeated; %d existing table fingerprints unchanged; SQLite integrity ok", len(before)))
}
