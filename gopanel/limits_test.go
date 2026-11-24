package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedQuotaCommitsBeforeGrant(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE limit_usage(user_id INTEGER PRIMARY KEY,total_bytes INTEGER);
		CREATE TABLE limit_daily(user_id INTEGER,day TEXT,bytes INTEGER,PRIMARY KEY(user_id,day));`); err != nil {
		t.Fatal(err)
	}
	app := &panel{queue: make(chan writeJob, 64)}
	done := make(chan struct{})
	go app.runWriter(db, done)
	defer func() { close(app.queue); <-done }()
	gate := &gateway{app: app, ready: time.Now()}
	entry := &account{current: row{"enabled": int64(1), "total_bytes": int64(100)}, usage: usage{id: 1, day: shanghaiDay()}}
	ctx := context.Background()
	var granted atomic.Int64
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() {
			for {
				bytes, err := gate.take(ctx, entry, 17, false)
				if errors.Is(err, quotaExceeded) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				granted.Add(int64(bytes))
			}
		})
	}
	writers.Wait()
	var persisted int64
	if err := db.QueryRow("SELECT total_bytes FROM limit_usage WHERE user_id=1").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 100 || granted.Load() != 100 {
		t.Fatalf("persisted=%d granted=%d", persisted, granted.Load())
	}
	if _, err := db.Exec("DROP TABLE limit_usage"); err != nil {
		t.Fatal(err)
	}
	entry.current["total_bytes"] = int64(200)
	if bytes, err := gate.take(ctx, entry, 10, false); err == nil || bytes != 0 || entry.usage.total != 100 {
		t.Fatalf("failed persistence granted traffic: bytes=%d usage=%d err=%v", bytes, entry.usage.total, err)
	}
}
