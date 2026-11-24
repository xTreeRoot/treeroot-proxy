package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type row map[string]any

func (item row) number(key string) int64 { value, _ := item[key].(int64); return value }
func (item row) text(key string) string  { value, _ := item[key].(string); return value }
func (item row) fields(keys ...string) row {
	result := make(row, len(keys))
	for _, key := range keys {
		result[key] = item[key]
	}
	return result
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func query(ctx context.Context, db querier, statement string, args ...any) ([]row, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for index := range values {
		targets[index] = &values[index]
	}
	result := []row{}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		item := make(row, len(columns))
		for index, column := range columns {
			item[column] = values[index]
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func one(ctx context.Context, db querier, statement string, args ...any) (row, error) {
	rows, err := query(ctx, db, statement, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

type writeJob struct {
	ctx      context.Context
	run      func(*sql.Tx) error
	rollback func()
	after    func()
	done     chan error
}

func (app *panel) write(ctx context.Context, run func(*sql.Tx) error) error {
	return app.submit(writeJob{ctx: ctx, run: run})
}

func (app *panel) submit(job writeJob) error {
	job.done = make(chan error, 1)
	app.queueMu.RLock()
	if app.closing {
		app.queueMu.RUnlock()
		return failure(503, "服务正在关闭")
	}
	select {
	case app.queue <- job:
		app.queueMu.RUnlock()
	default:
		app.queueMu.RUnlock()
		return failure(503, "写入繁忙，请稍后重试")
	}
	return <-job.done
}

func (app *panel) runWriter(db *sql.DB, done chan struct{}) {
	defer close(done)
	for job := range app.queue {
		err := transact(db, job)
		if err == nil && job.after != nil {
			job.after()
		}
		job.done <- err
	}
}

func transact(db *sql.DB, job writeJob) error {
	if err := job.ctx.Err(); err != nil {
		return err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = job.run(tx); err == nil {
		err = tx.Commit()
	}
	if err != nil && job.rollback != nil {
		job.rollback()
	}
	return err
}

func newPanel(cfg config, root string) (*panel, func(), error) {
	versionSource, err := os.ReadFile(filepath.Join(root, "connector_version.py"))
	if err != nil {
		return nil, nil, err
	}
	_, version, found := strings.Cut(string(versionSource), "CONNECTOR_VERSION = ")
	if !found {
		return nil, nil, errors.New("missing connector version")
	}
	writes, err := openDB(cfg, "rw", 1)
	if err != nil {
		return nil, nil, err
	}
	if err := migrate(writes); err != nil {
		writes.Close()
		return nil, nil, err
	}
	reads, err := openDB(cfg, "ro", 4)
	if err != nil {
		writes.Close()
		return nil, nil, err
	}
	app := &panel{config: cfg, root: root, version: strings.Trim(strings.TrimSpace(version), "'\""), database: reads, baseURL: cfg.BaseURL, queue: make(chan writeJob, 64), reportSlots: make(chan struct{}, 4), hashSlots: make(chan struct{}, 2), mux: http.NewServeMux(), live: make(map[string]liveSample)}
	app.routes()
	done := make(chan struct{})
	go app.runWriter(writes, done)
	return app, func() {
		app.queueMu.Lock()
		app.closing = true
		close(app.queue)
		app.queueMu.Unlock()
		<-done
		reads.Close()
		writes.Close()
	}, nil
}

func migrate(db *sql.DB) error {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("database must be in WAL mode")
	}
	ctx := context.Background()
	return transact(db, writeJob{ctx: ctx, run: func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS user_archives(
            user_id INTEGER PRIMARY KEY REFERENCES users(id),deleted_at INTEGER NOT NULL);
            CREATE TABLE IF NOT EXISTS egress_totals(
			device_id TEXT PRIMARY KEY REFERENCES egress_devices(id),upload INTEGER NOT NULL,
			download INTEGER NOT NULL,connections INTEGER NOT NULL,last_event_id INTEGER NOT NULL,since INTEGER NOT NULL);
			CREATE INDEX IF NOT EXISTS egress_request_cursor ON egress_events(device_id,id) WHERE kind='request';
			CREATE INDEX IF NOT EXISTS egress_events_device_time ON egress_events(device_id,ts);
			CREATE INDEX IF NOT EXISTS traffic_user_time ON traffic(user_id,ts);
			CREATE INDEX IF NOT EXISTS login_attempts_ip_time ON login_attempts(ip,ts);`)
		if err != nil {
			return err
		}
		return syncTotals(tx, "")
	}})
}

func dailyBackup(cfg config) error {
	folder := filepath.Join(cfg.DataDir, "backups")
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(folder, ".panel-*")
	if err != nil {
		return err
	}
	name := file.Name()
	file.Close()
	defer os.Remove(name)
	db, err := openDB(cfg, "ro", 1)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM INTO ?", name); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(folder, "panel-"+time.Now().UTC().Format(time.DateOnly)+".sqlite3")); err != nil {
		return err
	}
	old, err := filepath.Glob(filepath.Join(folder, "panel-*.sqlite3"))
	if err != nil {
		return err
	}
	for _, path := range old[:max(0, len(old)-7)] {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func logError(operation string, err error) {
	if err != nil {
		log.Printf("%s: %v", operation, err)
	}
}
