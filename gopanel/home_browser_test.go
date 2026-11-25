package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	hg "treeroot/gopanel/homegateway"
)

// Opt-in disposable localhost UI fixture. Never opens the production database.
func TestHomeBrowserFixture(t *testing.T) {
	if os.Getenv("TREEROOT_HOME_UI") != "1" {
		t.Skip("manual browser fixture")
	}
	app, _ := homeFixture(t)
	app.hashSlots = make(chan struct{}, 2)
	if _, e := app.database.Exec(`CREATE TABLE admin(id INTEGER PRIMARY KEY,username TEXT,password_hash TEXT);CREATE TABLE login_attempts(ip TEXT,ts INTEGER);`); e != nil {
		t.Fatal(e)
	}
	password, e := passwordHash("preview-only", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = app.database.Exec("INSERT INTO admin VALUES(1,'admin',?)", password); e != nil {
		t.Fatal(e)
	}
	app.baseURL = "http://localhost:19994"
	app.mux.HandleFunc("GET /api/me", app.me)
	app.route("POST /api/login", false, app.login)
	app.route("POST /api/logout", true, app.logout)
	app.route("GET /api/rates", false, func(http.ResponseWriter, *http.Request) (any, error) {
		return row{"upload": 0, "download": 0, "collector": row{"last_poll": unixNow()}}, nil
	})
	app.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		switch name {
		case "/":
			name = "index.html"
		case "/assets/app.js":
			name = "app.js"
		case "/assets/style.css":
			name = "style.css"
		case "/assets/echarts.min.js":
			name = "echarts.min.js"
		default:
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("..", name))
	})
	c := hg.Config{Revision: 1, Default: "direct", Devices: []hg.DevicePolicy{}, Rules: []hg.Rule{}, Nodes: []hg.Node{}}
	raw, _ := json.Marshal(c)
	e = app.write(context.Background(), func(tx *sql.Tx) error {
		_, e := tx.Exec("INSERT INTO home_gateways(id,name,created,last_seen,status,capabilities) VALUES('browser-lab','隔离测试网关',?, ?, ?, ?)", unixNow(), unixNow()+3600, `{"proxy_ready":true,"lan":"br-lan","wifi":{"online":true,"unified":true,"band2":{"enabled":true,"ssid":"UI-test-only","secured":true,"channel":0},"band5":{"enabled":true,"ssid":"UI-test-only","secured":true,"channel":0},"guest_supported":false}}`, `{"mode":"isolated","wifi":true,"ap_reboot":true,"limits":false,"upgrade":false}`)
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO home_configs VALUES('browser-lab',1,?,?)", string(raw), unixNow())
		if e != nil {
			return e
		}
		for i, m := range []string{"52:54:00:77:01:01", "52:54:00:77:01:02", "52:54:00:77:01:03"} {
			d := hg.Device{MAC: m, Name: []string{"隔离测试 A", "隔离测试 B", "隔离测试 C"}[i], IP: []string{"192.168.77.101", "192.168.77.102", "192.168.77.103"}[i], LastSeen: unixNow(), Upload: 1200, Download: 2400}
			body, _ := json.Marshal(d)
			if _, e = tx.Exec("INSERT INTO home_devices VALUES('browser-lab',?,?,?,?,?)", m, string(body), d.LastSeen, d.Upload, d.Download); e != nil {
				return e
			}
		}
		for i := range 65 {
			ev := hg.Event{ID: fmt.Sprint("ui-test-", i), TS: unixNow() - int64(i), MAC: "52:54:00:77:01:01", Target: "example.com", Port: 443, Protocol: "tcp", Action: "direct", Rule: "UI fixture"}
			body, _ := json.Marshal(ev)
			if _, e = tx.Exec("INSERT INTO home_events VALUES('browser-lab',?,?,?,?,?,?,?)", ev.ID, ev.TS, ev.MAC, ev.Target, ev.Rule, ev.Action, string(body)); e != nil {
				return e
			}
		}
		for i, route := range []string{"direct", "proxy"} {
			if _, e = tx.Exec("INSERT INTO home_traffic_totals VALUES('browser-lab',?,?,?,?,?,?,?)", route, (i+1)*1048576, (i+1)*5242880, unixNow()-86400, unixNow(), 1024, 4096); e != nil {
				return e
			}
			for h := range 24 {
				if _, e = tx.Exec("INSERT INTO home_traffic_minutes VALUES('browser-lab',?,?,?,?)", route, unixNow()/3600*3600-int64(h)*3600, 1024*(h+1), 4096*(h+1)*(i+1)); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Log("Disposable UI fixture: http://localhost:19994/#home")
	server := &http.Server{Addr: "127.0.0.1:19994", Handler: app}
	t.Cleanup(func() { server.Close() })
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		t.Fatal(e)
	}
}
