package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hg "treeroot/gopanel/homegateway"
)

// Opt-in fixture for the real OpenWrt agent. The production DB is read-only;
// all pairing, policies, events and task changes use a disposable database.
func TestHomeAgentVPSFixture(t *testing.T) {
	dir := os.Getenv("TREEROOT_HOME_E2E_DIR")
	if dir == "" {
		t.Skip("manual OpenWrt integration fixture")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute private fixture directory required")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	app, req := homeFixture(t)
	if _, e := app.database.Exec("UPDATE sessions SET expires=?", unixNow()+7200); e != nil {
		t.Fatal(e)
	}
	if source := os.Getenv("TREEROOT_HOME_E2E_SOURCE"); source != "" {
		cfg, e := readConfig(source)
		if e != nil {
			t.Fatal("cannot read source configuration")
		}
		db, e := openDB(cfg, "ro", 1)
		if e != nil {
			t.Fatal("cannot open read-only source")
		}
		defer db.Close()
		var uuid string
		e = db.QueryRow(`SELECT u.client_uuid FROM users u LEFT JOIN user_archives a ON a.user_id=u.id WHERE u.enabled=1 AND a.user_id IS NULL ORDER BY u.id LIMIT 1`).Scan(&uuid)
		if e != nil {
			t.Fatal("no enabled source proxy identity")
		}
		if _, e = app.database.Exec("UPDATE users SET client_uuid=? WHERE id=1", uuid); e != nil {
			t.Fatal("cannot prepare disposable proxy identity")
		}
		app.baseURL, app.config = cfg.BaseURL, cfg
	}
	status, response := req("POST", "/api/home", map[string]string{"name": "隔离验收网关"}, true)
	if status != 200 {
		t.Fatal("fixture gateway creation failed")
	}
	id := response["id"].(string)
	c := hg.Config{Revision: 1, Default: "direct", Devices: []hg.DevicePolicy{{MAC: "52:54:00:77:01:01", FixedIP: "192.168.77.101"}, {MAC: "52:54:00:77:01:02", FixedIP: "192.168.77.102"}}, Rules: []hg.Rule{}, Nodes: []hg.Node{}}
	if status, _ = req("PUT", "/api/home/"+id+"/config", c, true); status != 200 {
		t.Fatal("fixture initial policy failed")
	}
	status, response = req("POST", "/api/home/"+id+"/pair-code", nil, true)
	if status != 200 {
		t.Fatal("fixture pairing code failed")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:19996")
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(app)
	server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	cert, e := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if e = os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0600); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(map[string]string{"server": "https://127.0.0.1:19996", "id": id, "code": response["code"].(string), "token": "abcdefghijklmnopqrstuvwxyz0123456789"})
	if e = os.WriteFile(filepath.Join(dir, "connection.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Isolated TLS candidate listening on loopback:19996")
	<-make(chan struct{})
}
