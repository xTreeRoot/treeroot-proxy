package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	hg "treeroot/gopanel/homegateway"
)

func TestEachGatewayOwnsDistinctProxyIdentityAndFailedCreateRollsBack(t *testing.T) {
	app, req := homeFixture(t)
	firstID := int64(0)
	for _, name := range []string{"home A", "home B"} {
		code, v := req("POST", "/api/home", map[string]string{"name": name}, true)
		if code != 200 {
			t.Fatal(code, v)
		}
		id := v["id"].(string)
		u, e := gatewayProxyUser(context.Background(), app.database, id)
		if e != nil || u.number("id") == 1 || u.number("id") == firstID {
			t.Fatal("shared identity", u, e)
		}
		firstID = u.number("id")
		c, e := homeConfig(context.Background(), app.database, id, 1)
		if e != nil || c.Default != "proxy" || len(c.Nodes) != 1 || c.DefaultNode != c.Nodes[0].ID {
			t.Fatal(c, e)
		}
		code, v = req("POST", "/api/home/"+id+"/proxy-user", nil, true)
		if code != 200 || int64(v["user_id"].(float64)) != firstID {
			t.Fatal("non-idempotent binding", code, v)
		}
		c.Nodes = []hg.Node{{ID: "user-1"}}
		c.DefaultNode = "user-1"
		if code, _ = req("PUT", "/api/home/"+id+"/config", c, true); code != 422 {
			t.Fatal("different user's statistics accepted", code)
		}
	}
	var before int
	app.database.QueryRow("SELECT COUNT(*) FROM users").Scan(&before)
	raw, e := os.ReadFile(app.config.XrayConfig)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("TREEROOT_TEST_XRAY_FAIL", "1")
	code, v := req("POST", "/api/home", map[string]string{"name": "failing"}, true)
	if code != 503 {
		t.Fatal(code, v)
	}
	var count int
	app.database.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != before {
		t.Fatal("orphan account after rollback")
	}
	app.database.QueryRow("SELECT COUNT(*) FROM home_gateways").Scan(&count)
	if count != 2 {
		t.Fatal("orphan gateway after rollback")
	}
	after, _ := os.ReadFile(app.config.XrayConfig)
	if string(raw) != string(after) {
		t.Fatal("Xray not restored")
	}
}
func TestLegacyGatewayBindingPreservesHistoryAndRetargetsProxyRules(t *testing.T) {
	app, req := homeFixture(t)
	c := hg.Config{Revision: 1, Default: "proxy", DefaultNode: "user-1", Nodes: []hg.Node{{ID: "user-1"}}, Rules: []hg.Rule{{ID: "proxy", Scope: "all", TargetType: "domain", Target: "google.com", Action: "proxy", Node: "user-1", Enabled: true}, {ID: "direct", Scope: "all", TargetType: "suffix", Target: "cn", Action: "direct", Enabled: true}}}
	raw, _ := json.Marshal(c)
	if _, e := app.database.Exec("INSERT INTO home_gateways(id,name,created) VALUES('legacy','existing',?)", unixNow()); e != nil {
		t.Fatal(e)
	}
	if _, e := app.database.Exec("INSERT INTO home_configs VALUES('legacy',1,?,?)", string(raw), unixNow()); e != nil {
		t.Fatal(e)
	}
	if _, e := app.database.Exec("UPDATE users SET upload=123,download=456 WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	code, v := req("POST", "/api/home/legacy/proxy-user", nil, true)
	if code != 200 {
		t.Fatal(code, v)
	}
	current, e := homeConfig(context.Background(), app.database, "legacy", 2)
	if e != nil || current.DefaultNode != "user-2" || current.Rules[0].Node != "user-2" || current.Rules[1].Target != "cn" {
		t.Fatal(current, e)
	}
	var total int
	app.database.QueryRow("SELECT upload+download FROM users WHERE id=1").Scan(&total)
	if total != 579 {
		t.Fatal("old history moved or duplicated")
	}
	app.route("DELETE /api/users/{id}", true, app.changeUser)
	app.route("POST /api/users/{id}/reset", true, app.changeUser)
	for _, item := range []struct{ method, path string }{{"DELETE", "/api/users/2"}, {"POST", "/api/users/2/reset"}} {
		if code, _ = req(item.method, item.path, nil, true); code != 409 {
			t.Fatal("gateway account was destructively changed", code)
		}
	}
}
