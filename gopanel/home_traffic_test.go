package main

import (
	"encoding/json"
	"testing"
	"time"
	hg "treeroot/gopanel/homegateway"
)

func TestHomeTrafficCumulativeCheckpointsRatesAndReplays(t *testing.T) {
	app, req := homeFixture(t)
	_, d := req("POST", "/api/home", map[string]string{"name": "meter"}, true)
	id := d["id"].(string)
	path := "/api/home/" + id
	if _, e := app.database.Exec("UPDATE home_gateways SET agent_hash=? WHERE id=?", digest("abcdefghijklmnopqrstuvwxyz0123456789"), id); e != nil {
		t.Fatal(e)
	}
	at := time.Now().Add(-10 * time.Second).UnixMilli()
	s := hg.Sync{Boot: "meter-boot-0123456789", Sequence: 1, Traffic: &hg.TrafficSample{Generation: "generation-0123456789", At: at, Monotonic: 1000, Started: at / 1000, Routes: []hg.RouteTraffic{{Route: "direct", Upload: 100, Download: 200}, {Route: "proxy", Upload: 1000, Download: 2000}}}}
	send := func() {
		t.Helper()
		code, v := req("POST", path+"/sync", s, false)
		if code != 200 {
			t.Fatal(code, v)
		}
	}
	send()
	send()
	code, v := req("GET", path+"/overview", nil, true)
	if code != 200 {
		t.Fatal(code, v)
	}
	totals := v["totals"].([]any)
	if totals[0].(map[string]any)["upload"] != float64(100) || totals[0].(map[string]any)["speed_upload"] != nil {
		t.Fatal(v)
	}
	s.Sequence++
	s.Traffic.At += 5000
	s.Traffic.Monotonic += 5000
	s.Traffic.Routes[0].Upload += 50
	s.Traffic.Routes[0].Download += 100
	send()
	send()
	_, v = req("GET", path+"/overview?hours=168", nil, true)
	totals = v["totals"].([]any)
	direct := totals[0].(map[string]any)
	if direct["upload"] != float64(150) || direct["day_download"] != float64(300) || direct["speed_upload"] != float64(10) || len(v["series"].([]any)) < 168 {
		t.Fatal(v)
	}
	// Restart establishes a fresh rate baseline while retaining cumulative bytes.
	s.Boot = "rebooted-0123456789"
	s.Sequence = 1
	s.Traffic.At += 1000
	s.Traffic.Monotonic = 100
	s.Traffic.Routes[0].Upload += 10
	send()
	_, v = req("GET", path+"/overview", nil, true)
	direct = v["totals"].([]any)[0].(map[string]any)
	if direct["upload"] != float64(160) || direct["speed_upload"] != nil {
		t.Fatal(direct)
	}
	// A new local ledger can start from zero without reducing the server total.
	s.Sequence++
	s.Traffic.Generation = "replacement-0123456789"
	s.Traffic.At++
	s.Traffic.Routes[0].Upload = 20
	s.Traffic.Routes[0].Download = 30
	send()
	send()
	_, v = req("GET", path+"/overview", nil, true)
	direct = v["totals"].([]any)[0].(map[string]any)
	if direct["upload"] != float64(180) || direct["download"] != float64(330) {
		t.Fatal(direct)
	}
	if code, _ = req("GET", path+"/overview", nil, false); code != 401 {
		t.Fatal("public statistics", code)
	}
	s.Sequence++
	s.Traffic.Routes[0].Upload = -1
	if code, _ = req("POST", path+"/sync", s, false); code != 422 {
		t.Fatal("negative bytes", code)
	}
	if _, e := app.database.Exec("UPDATE home_gateways SET last_seen=? WHERE id=?", unixNow()-60, id); e != nil {
		t.Fatal(e)
	}
	_, v = req("GET", path+"/overview", nil, true)
	for _, v := range v["totals"].([]any) {
		if v.(map[string]any)["speed_upload"] != nil {
			t.Fatal("offline rate", v)
		}
	}
}

func TestChinaTemplateAndRouteFiltering(t *testing.T) {
	app, req := homeFixture(t)
	_, d := req("POST", "/api/home", map[string]string{"name": "china"}, true)
	id := d["id"].(string)
	path := "/api/home/" + id
	code, v := req("POST", path+"/rules/preview", map[string]any{"revision": 1, "template": "china-direct"}, true)
	if code != 200 {
		t.Fatal(code, v)
	}
	raw, _ := json.Marshal(v["config"])
	var c hg.Config
	if json.Unmarshal(raw, &c) != nil || len(c.Rules) != 32 || c.Default != "proxy" || c.DefaultNode != "user-2" {
		t.Fatal(v)
	}
	compiled, e := c.CompileRules(nil)
	if e != nil || compiled[len(compiled)-2] != "GEOIP,CN,DIRECT" || compiled[len(compiled)-1] != "MATCH,HG-user-2" {
		t.Fatal(compiled, e)
	}
	var count int
	app.database.QueryRow("SELECT COUNT(*) FROM home_configs WHERE gateway_id=?", id).Scan(&count)
	if count != 1 {
		t.Fatal("preview mutated configuration")
	}
	for _, action := range []string{"direct", "proxy", "reject"} {
		ev := hg.Event{ID: action, TS: unixNow(), Action: action, Target: action + ".test"}
		body, _ := json.Marshal(ev)
		if _, e = app.database.Exec("INSERT INTO home_events VALUES(?,?,?,?,?,?,?,?)", id, ev.ID, ev.TS, "", ev.Target, "", action, string(body)); e != nil {
			t.Fatal(e)
		}
	}
	code, v = req("GET", path+"/events?action=proxy", nil, true)
	if code != 200 || v["total"] != float64(1) {
		t.Fatal(code, v)
	}
	if code, _ = req("GET", path+"/events?action=unknown", nil, true); code != 422 {
		t.Fatal(code)
	}
}
