package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	hg "treeroot/gopanel/homegateway"
)

func homeFixture(t *testing.T) (*panel, func(string, string, any, bool) (int, map[string]any)) {
	t.Helper()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "home.db"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY,name TEXT,client_uuid TEXT,email TEXT,token TEXT,created INTEGER,upload INTEGER DEFAULT 0,download INTEGER DEFAULT 0,enabled INTEGER DEFAULT 1);CREATE TABLE audit(id INTEGER PRIMARY KEY,ts INTEGER,action TEXT,user_id INTEGER);CREATE TABLE user_archives(user_id INTEGER PRIMARY KEY,deleted_at INTEGER);CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,csrf TEXT,expires INTEGER);` + homeSchema)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO users(id,name,client_uuid,enabled) VALUES(1,'test','private-test-node',1);INSERT INTO sessions VALUES(?,?,?)", digest("session"), "csrf", unixNow()+600)
	if e != nil {
		t.Fatal(e)
	}
	app := &panel{database: db, queue: make(chan writeJob, 64), mux: http.NewServeMux(), baseURL: "https://example.test", config: config{WSPath: "/ws"}, reportSlots: make(chan struct{}, 4)}
	app.config.DataDir = t.TempDir()
	app.config.XrayConfig = filepath.Join(app.config.DataDir, "xray.json")
	if e = os.WriteFile(app.config.XrayConfig, []byte(`{"inbounds":[{"tag":"websocket-in","settings":{"clients":[]}}],"outbounds":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	app.config.Xray, e = os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	app.routesHome()
	done := make(chan struct{})
	go app.runWriter(db, done)
	t.Cleanup(func() { close(app.queue); <-done; db.Close() })
	return app, func(method, path string, body any, auth bool) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if auth {
			r.AddCookie(&http.Cookie{Name: "__Host-treeroot", Value: "session"})
			r.Header.Set("X-CSRF-Token", "csrf")
		} else {
			r.Header.Set("Authorization", "Bearer "+"abcdefghijklmnopqrstuvwxyz0123456789")
		}
		w := httptest.NewRecorder()
		app.mux.ServeHTTP(w, r)
		var data map[string]any
		json.Unmarshal(w.Body.Bytes(), &data)
		return w.Code, data
	}
}
func TestHomePairSyncAndRevoke(t *testing.T) {
	app, req := homeFixture(t)
	code, _ := req("POST", "/api/home", map[string]any{"name": "lab"}, false)
	if code != 401 {
		t.Fatal(code)
	}
	code, d := req("POST", "/api/home", map[string]any{"name": "lab"}, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	id := d["id"].(string)
	path := "/api/home/" + id
	code, d = req("POST", path+"/pair-code", nil, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	pair := map[string]any{"code": d["code"], "token": "abcdefghijklmnopqrstuvwxyz0123456789"}
	code, d = req("POST", path+"/pair", pair, false)
	if code != 200 {
		t.Fatal(code, d)
	}
	code, _ = req("POST", path+"/pair", pair, false)
	if code != 200 {
		t.Fatal("pair retry", code)
	}
	s := hg.Sync{Boot: "boot-0123456789abcdef", Sequence: 1, Version: "test", Status: map[string]any{"mode": "isolated"}, Devices: []hg.Device{{MAC: "52:54:00:77:01:01", IP: "192.168.77.101", Upload: 100, Download: 200}}, Events: []hg.Event{{ID: "event-1", TS: unixNow(), MAC: "52:54:00:77:01:01", Target: "example.com"}}}
	for range 2 {
		code, d = req("POST", path+"/sync", s, false)
		if code != 200 {
			t.Fatal(code, d)
		}
	}
	var count int
	app.database.QueryRow("SELECT COUNT(*) FROM home_events").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate replay", count)
	}
	code, d = req("POST", path+"/tasks", map[string]any{"kind": "arbitrary.shell"}, true)
	if code != 422 {
		t.Fatal(code, d)
	}
	code, d = req("POST", path+"/revoke", nil, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	s.Sequence++
	code, _ = req("POST", path+"/sync", s, false)
	if code != 401 {
		t.Fatal("revoked token accepted", code)
	}
}
func TestHomeVersionConflictAndCredentialBoundary(t *testing.T) {
	app, req := homeFixture(t)
	_, d := req("POST", "/api/home", map[string]any{"name": "lab"}, true)
	id := d["id"].(string)
	path := "/api/home/" + id
	c := hg.Config{Revision: 1, Default: "proxy", DefaultNode: "user-2", Nodes: []hg.Node{{ID: "user-2", Proxy: map[string]any{"password": "must-not-store"}}}}
	code, d := req("PUT", path+"/config", c, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	code, _ = req("PUT", path+"/config", c, true)
	if code != 409 {
		t.Fatal("stale overwrite", code)
	}
	var raw string
	app.database.QueryRow("SELECT body FROM home_configs WHERE gateway_id=? AND revision=2", id).Scan(&raw)
	if bytes.Contains([]byte(raw), []byte("password")) {
		t.Fatal("stored caller credentials")
	}
	code, d = req("GET", path+"/config", nil, true)
	out, _ := json.Marshal(d)
	if code != 200 || bytes.Contains(out, []byte("private-test-node")) {
		t.Fatal(code, d)
	}
	_, e := app.database.Exec("UPDATE users SET enabled=0 WHERE id=2")
	if e != nil {
		t.Fatal(e)
	}
	c.Revision = 2
	code, _ = req("PUT", path+"/config", c, true)
	if code != 422 {
		t.Fatal("disabled proxy user accepted", code)
	}
	_, e = app.database.Exec("UPDATE home_gateways SET agent_hash=? WHERE id=?", digest("abcdefghijklmnopqrstuvwxyz0123456789"), id)
	if e != nil {
		t.Fatal(e)
	}
	code, d = req("POST", path+"/sync", hg.Sync{Boot: "boot-disabled-node", Sequence: 1, Devices: []hg.Device{{MAC: "52:54:00:77:01:01", Upload: 123}}, Events: []hg.Event{{ID: "still-recorded", TS: unixNow(), Target: "example.com"}}}, false)
	if code != 200 || d["config"] != nil || d["config_error"] == "" || d["accepted"] != float64(1) {
		t.Fatal("disabled node interrupted management", code, d)
	}
	var total int64
	app.database.QueryRow("SELECT upload FROM home_devices WHERE gateway_id=?", id).Scan(&total)
	if total != 123 {
		t.Fatal("telemetry rolled back", total)
	}
}

func TestHomeAdvancedPreviewUsesExistingSaveAndVersionChecks(t *testing.T) {
	app, req := homeFixture(t)
	_, created := req("POST", "/api/home", map[string]any{"name": "advanced-lab"}, true)
	id := created["id"].(string)
	path := "/api/home/" + id
	body := map[string]any{"revision": 1, "scope": "device", "subject": "52:54:00:77:01:01", "source": "prepend: ['DOMAIN-KEYWORD,treeroot.top,DIRECT', 'DOMAIN,example.com,REJECT']\nappend: []\ndelete: []"}
	if code, _ := req("POST", path+"/rules/preview", body, false); code != 401 {
		t.Fatal("preview requires login", code)
	}
	code, preview := req("POST", path+"/rules/preview", body, true)
	if code != 200 {
		t.Fatal(code, preview)
	}
	var revision, count int
	app.database.QueryRow("SELECT revision FROM home_gateways WHERE id=?", id).Scan(&revision)
	app.database.QueryRow("SELECT COUNT(*) FROM home_configs WHERE gateway_id=?", id).Scan(&count)
	if revision != 1 || count != 1 {
		t.Fatal("preview wrote configuration")
	}
	code, result := req("PUT", path+"/config", preview["config"], true)
	if code != 200 || result["revision"] != float64(2) {
		t.Fatal(code, result)
	}
	if code, _ = req("POST", path+"/rules/preview", body, true); code != 409 {
		t.Fatal("stale preview accepted", code)
	}
	body["revision"] = 2
	body["source"] = "prepend: ['SCRIPT,bad,DIRECT']"
	if code, _ = req("POST", path+"/rules/preview", body, true); code != 422 {
		t.Fatal("unsupported rule accepted", code)
	}
	app.database.QueryRow("SELECT COUNT(*) FROM home_configs WHERE gateway_id=?", id).Scan(&count)
	if count != 2 {
		t.Fatal("failed preview changed config")
	}
}

func TestHomeRollbackCreatesNewRevisionAndExpiredTaskNeverRuns(t *testing.T) {
	app, req := homeFixture(t)
	code, d := req("POST", "/api/home", map[string]any{"name": "lab"}, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	id := d["id"].(string)
	path := "/api/home/" + id
	c := hg.Config{Revision: 1, Default: "reject", Devices: []hg.DevicePolicy{}, Rules: []hg.Rule{}, Nodes: []hg.Node{}}
	code, d = req("PUT", path+"/config", c, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	code, d = req("POST", path+"/rollback", map[string]int{"revision": 1, "current": 2}, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	restored, e := homeConfig(context.Background(), app.database, id, 3)
	if e != nil || restored.Default != "proxy" {
		t.Fatal(restored, e)
	}
	_, e = app.database.Exec("UPDATE home_gateways SET agent_hash=?,last_seen=? WHERE id=?", digest("abcdefghijklmnopqrstuvwxyz0123456789"), unixNow(), id)
	if e != nil {
		t.Fatal(e)
	}
	_, e = app.database.Exec("INSERT INTO home_tasks(id,gateway_id,kind,created,expires,state,payload) VALUES('expired',?,'gateway.reboot',?,?,'pending','{}')", id, unixNow()-120, unixNow()-60)
	if e != nil {
		t.Fatal(e)
	}
	code, d = req("POST", path+"/sync", hg.Sync{Boot: "boot-0123456789abcdef", Sequence: 1}, false)
	if code != 200 {
		t.Fatal(code, d)
	}
	if tasks := d["tasks"].([]any); len(tasks) != 0 {
		t.Fatal("expired task returned", tasks)
	}
	var state string
	app.database.QueryRow("SELECT state FROM home_tasks WHERE id='expired'").Scan(&state)
	if state != "expired" {
		t.Fatal(state)
	}
}

func TestHomeBackupRestorePublishesHistoricalPolicyAsNewRevision(t *testing.T) {
	app, req := homeFixture(t)
	_, d := req("POST", "/api/home", map[string]string{"name": "lab"}, true)
	id := d["id"].(string)
	path := "/api/home/" + id
	if _, e := app.database.Exec("UPDATE home_gateways SET last_seen=? WHERE id=?", unixNow(), id); e != nil {
		t.Fatal(e)
	}
	code, d := req("POST", path+"/tasks", map[string]string{"kind": "backup.create"}, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	backupID := d["id"].(string)
	if _, e := app.database.Exec("UPDATE home_tasks SET state='success' WHERE id=?", backupID); e != nil {
		t.Fatal(e)
	}
	c := hg.Config{Revision: 1, Default: "reject"}
	if code, d = req("PUT", path+"/config", c, true); code != 200 {
		t.Fatal(code, d)
	}
	code, d = req("POST", path+"/tasks", map[string]any{"kind": "backup.restore", "payload": map[string]string{"backup_id": backupID}}, true)
	if code != 200 {
		t.Fatal(code, d)
	}
	restored, e := homeConfig(context.Background(), app.database, id, 3)
	if e != nil || restored.Default != "proxy" {
		t.Fatal("backup did not create restored desired revision", restored, e)
	}
}
