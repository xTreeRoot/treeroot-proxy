package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"treeroot/gopanel/homeap"
	hg "treeroot/gopanel/homegateway"
)

const homeSchema = `
CREATE TABLE IF NOT EXISTS home_gateways(
 id TEXT PRIMARY KEY,name TEXT NOT NULL,created INTEGER NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,
 agent_hash TEXT NOT NULL DEFAULT '',pair_hash TEXT NOT NULL DEFAULT '',pair_expires INTEGER NOT NULL DEFAULT 0,
 last_seen INTEGER NOT NULL DEFAULT 0,version TEXT NOT NULL DEFAULT '',applied INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT '{}',capabilities TEXT NOT NULL DEFAULT '{}',revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS home_configs(gateway_id TEXT NOT NULL REFERENCES home_gateways(id),revision INTEGER NOT NULL,body TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(gateway_id,revision));
CREATE TABLE IF NOT EXISTS home_boots(gateway_id TEXT NOT NULL REFERENCES home_gateways(id),boot TEXT NOT NULL,sequence INTEGER NOT NULL,PRIMARY KEY(gateway_id,boot));
CREATE TABLE IF NOT EXISTS home_devices(gateway_id TEXT NOT NULL REFERENCES home_gateways(id),mac TEXT NOT NULL,body TEXT NOT NULL,last_seen INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(gateway_id,mac));
CREATE TABLE IF NOT EXISTS home_events(gateway_id TEXT NOT NULL REFERENCES home_gateways(id),event_id TEXT NOT NULL,ts INTEGER NOT NULL,mac TEXT NOT NULL,target TEXT NOT NULL,rule_id TEXT NOT NULL,action TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(gateway_id,event_id));
CREATE INDEX IF NOT EXISTS home_events_time ON home_events(gateway_id,ts DESC);
CREATE TABLE IF NOT EXISTS home_tasks(id TEXT PRIMARY KEY,gateway_id TEXT NOT NULL REFERENCES home_gateways(id),kind TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',finished INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS home_tasks_pending ON home_tasks(gateway_id,state,expires);
CREATE TABLE IF NOT EXISTS home_audit(id INTEGER PRIMARY KEY,gateway_id TEXT NOT NULL,ts INTEGER NOT NULL,action TEXT NOT NULL,detail TEXT NOT NULL);
`

func (app *panel) routesHome() {
	app.route("GET /api/home", true, app.report(app.homeList))
	app.route("POST /api/home", true, app.homeCreate)
	app.route("GET /api/home/nodes", true, app.report(func(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
		return query(ctx, tx, `SELECT u.id,u.name FROM users u LEFT JOIN user_archives a ON a.user_id=u.id WHERE u.enabled=1 AND a.user_id IS NULL ORDER BY u.id`)
	}))
	app.route("POST /api/home/{id}/pair-code", true, app.homePairCode)
	app.route("POST /api/home/{id}/revoke", true, app.homeRevoke)
	app.route("POST /api/home/{id}/pair", false, app.homePair)
	app.route("POST /api/home/{id}/sync", false, app.homeSync)
	app.route("GET /api/home/{id}/config", true, app.report(app.homeGetConfig))
	app.route("PUT /api/home/{id}/config", true, app.homePutConfig)
	app.route("POST /api/home/{id}/rules/preview", true, app.report(app.homeRulesPreview))
	app.route("GET /api/home/{id}/devices", true, app.report(app.homeDevices))
	app.route("GET /api/home/{id}/events", true, app.report(app.homeEvents))
	app.route("GET /api/home/{id}/tasks", true, app.report(func(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
		return query(ctx, tx, "SELECT id,kind,created,expires,state,detail,finished FROM home_tasks WHERE gateway_id=? ORDER BY created DESC,id DESC LIMIT 100", r.PathValue("id"))
	}))
	app.route("POST /api/home/{id}/tasks", true, app.homeTask)
	app.route("GET /api/home/{id}/history", true, app.report(func(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
		return query(ctx, tx, "SELECT revision,created FROM home_configs WHERE gateway_id=? ORDER BY revision DESC LIMIT 100", r.PathValue("id"))
	}))
	app.route("POST /api/home/{id}/rollback", true, app.homeRollback)
}
func homeAudit(tx *sql.Tx, id, action, detail string) error {
	_, e := tx.Exec("INSERT INTO home_audit(gateway_id,ts,action,detail) VALUES(?,?,?,?)", id, unixNow(), action, detail)
	return e
}
func homeRow(ctx context.Context, db querier, id string) (row, error) {
	v, e := one(ctx, db, "SELECT * FROM home_gateways WHERE id=?", id)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, failure(404, "家庭网关不存在")
	}
	return v, e
}
func (app *panel) homeList(ctx context.Context, tx *sql.Tx, _ *http.Request) (any, error) {
	rows, e := query(ctx, tx, "SELECT id,name,created,enabled,agent_hash,last_seen,version,applied,error,status,capabilities,revision FROM home_gateways ORDER BY created,id")
	if e != nil {
		return nil, e
	}
	for _, v := range rows {
		v["online"] = v.number("last_seen") > unixNow()-30 && v.number("enabled") == 1
		v["paired"] = v.text("agent_hash") != ""
		delete(v, "agent_hash")
		v["enabled"] = v.number("enabled") == 1
		v["status"] = json.RawMessage(v.text("status"))
		v["capabilities"] = json.RawMessage(v.text("capabilities"))
	}
	return rows, nil
}
func (app *panel) homeCreate(_ http.ResponseWriter, r *http.Request) (any, error) {
	var body struct{ Name string }
	if e := decode(r, &body); e != nil {
		return nil, e
	}
	if !validName(body.Name) {
		return nil, failure(422, "请填写网关名称")
	}
	id := randomHex(16)
	c := hg.Config{Revision: 1, Default: "direct", Devices: []hg.DevicePolicy{}, Rules: []hg.Rule{}, Nodes: []hg.Node{}}
	raw, _ := json.Marshal(c)
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		v, e := one(r.Context(), tx, "SELECT COUNT(*) n FROM home_gateways")
		if e != nil {
			return e
		}
		if v.number("n") >= 16 {
			return failure(422, "网关数量达到上限")
		}
		if _, e = tx.Exec("INSERT INTO home_gateways(id,name,created) VALUES(?,?,?)", id, body.Name, unixNow()); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO home_configs VALUES(?,?,?,?)", id, 1, string(raw), unixNow()); e != nil {
			return e
		}
		return homeAudit(tx, id, "create", body.Name)
	})
	return row{"id": id}, e
}
func (app *panel) homePairCode(_ http.ResponseWriter, r *http.Request) (any, error) {
	code := randomToken()
	expires := unixNow() + 600
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		g, e := homeRow(r.Context(), tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if g.text("agent_hash") != "" {
			return failure(409, "先撤销现有配对")
		}
		if _, e = tx.Exec("UPDATE home_gateways SET enabled=1,pair_hash=?,pair_expires=? WHERE id=?", digest(code), expires, r.PathValue("id")); e != nil {
			return e
		}
		return homeAudit(tx, r.PathValue("id"), "pair-code", "")
	})
	return row{"code": code, "expires": expires, "id": r.PathValue("id"), "server": app.baseURL}, e
}
func (app *panel) homeRevoke(_ http.ResponseWriter, r *http.Request) (any, error) {
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		if _, e := homeRow(r.Context(), tx, r.PathValue("id")); e != nil {
			return e
		}
		if _, e := tx.Exec("UPDATE home_gateways SET enabled=0,agent_hash='',pair_hash='',pair_expires=0 WHERE id=?", r.PathValue("id")); e != nil {
			return e
		}
		if _, e := tx.Exec("UPDATE home_tasks SET state='cancelled',detail='配对已撤销',finished=? WHERE gateway_id=? AND state IN ('pending','running')", unixNow(), r.PathValue("id")); e != nil {
			return e
		}
		return homeAudit(tx, r.PathValue("id"), "revoke", "")
	})
	return row{"ok": e == nil}, e
}
func (app *panel) homePair(_ http.ResponseWriter, r *http.Request) (any, error) {
	var b struct{ Code, Token string }
	if e := decode(r, &b); e != nil {
		return nil, e
	}
	if len(b.Token) < 32 || len(b.Token) > 128 || len(b.Code) > 128 {
		return nil, failure(422, "配对参数无效")
	}
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		g, e := homeRow(r.Context(), tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if g.number("enabled") != 1 || g.number("pair_expires") < unixNow() || g.text("pair_hash") == "" || !same(g.text("pair_hash"), digest(b.Code)) {
			return failure(401, "配对码无效或已过期")
		}
		if g.text("agent_hash") != "" && !same(g.text("agent_hash"), digest(b.Token)) {
			return failure(409, "网关已配对")
		}
		_, e = tx.Exec("UPDATE home_gateways SET agent_hash=? WHERE id=?", digest(b.Token), r.PathValue("id"))
		return e
	})
	return row{"ok": e == nil}, e
}
func homeAuth(g row, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(token) > 128 || g.number("enabled") != 1 || g.text("agent_hash") == "" || !same(digest(token), g.text("agent_hash")) {
		return failure(401, "网关认证已失效")
	}
	return nil
}
func homeConfig(ctx context.Context, db querier, id string, revision int64) (hg.Config, error) {
	var c hg.Config
	v, e := one(ctx, db, "SELECT body FROM home_configs WHERE gateway_id=? AND revision=?", id, revision)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal([]byte(v.text("body")), &c)
	normalizeHomeConfig(&c)
	return c, e
}
func normalizeHomeConfig(c *hg.Config) {
	if c.Devices == nil {
		c.Devices = []hg.DevicePolicy{}
	}
	if c.Rules == nil {
		c.Rules = []hg.Rule{}
	}
	if c.Nodes == nil {
		c.Nodes = []hg.Node{}
	}
}
func (app *panel) homeNodes(ctx context.Context, db querier, c *hg.Config, private bool) error {
	base, e := url.Parse(app.baseURL)
	if e != nil {
		return e
	}
	for i, n := range c.Nodes {
		uid, e := strconv.ParseInt(strings.TrimPrefix(n.ID, "user-"), 10, 64)
		if e != nil {
			return failure(422, "节点标识无效")
		}
		v, e := one(ctx, db, `SELECT u.name,u.client_uuid FROM users u LEFT JOIN user_archives a ON a.user_id=u.id WHERE u.id=? AND u.enabled=1 AND a.user_id IS NULL`, uid)
		if errors.Is(e, sql.ErrNoRows) {
			return failure(422, "代理用户已停用或不存在")
		}
		if e != nil {
			return e
		}
		c.Nodes[i].Name = v.text("name")
		c.Nodes[i].Proxy = nil
		if private {
			c.Nodes[i].Proxy = map[string]any{"name": "HG-" + n.ID, "type": "vmess", "server": base.Hostname(), "port": 443, "uuid": v.text("client_uuid"), "alterId": 0, "cipher": "auto", "udp": true, "tls": true, "servername": base.Hostname(), "skip-cert-verify": false, "network": "ws", "ws-opts": row{"path": app.config.WSPath, "headers": row{"Host": base.Hostname()}}}
		}
	}
	return nil
}
func (app *panel) homeGetConfig(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	g, e := homeRow(ctx, tx, r.PathValue("id"))
	if e != nil {
		return nil, e
	}
	return homeConfig(ctx, tx, r.PathValue("id"), g.number("revision"))
}
func (app *panel) saveHomeConfig(ctx context.Context, tx *sql.Tx, id string, c hg.Config) error {
	normalizeHomeConfig(&c)
	g, e := homeRow(ctx, tx, id)
	if e != nil {
		return e
	}
	if c.Revision != g.number("revision") {
		return failure(409, "配置已更新，请刷新后重试")
	}
	if e = c.Validate(); e != nil {
		return failure(422, e.Error())
	}
	if e = app.homeNodes(ctx, tx, &c, false); e != nil {
		return e
	}
	c.Revision++
	raw, _ := json.Marshal(c)
	if _, e = tx.Exec("INSERT INTO home_configs VALUES(?,?,?,?)", id, c.Revision, string(raw), unixNow()); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE home_gateways SET revision=? WHERE id=?", c.Revision, id); e != nil {
		return e
	}
	return homeAudit(tx, id, "config", strconv.FormatInt(c.Revision, 10))
}
func (app *panel) homePutConfig(_ http.ResponseWriter, r *http.Request) (any, error) {
	var c hg.Config
	if e := decode(r, &c); e != nil {
		return nil, e
	}
	e := app.write(r.Context(), func(tx *sql.Tx) error { return app.saveHomeConfig(r.Context(), tx, r.PathValue("id"), c) })
	return row{"revision": c.Revision + 1, "state": "pending"}, e
}
func (app *panel) homeRulesPreview(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	var body struct {
		Revision int64  `json:"revision"`
		Source   string `json:"source"`
		Scope    string `json:"scope"`
		Subject  string `json:"subject"`
	}
	if e := decode(r, &body); e != nil {
		return nil, e
	}
	g, e := homeRow(ctx, tx, r.PathValue("id"))
	if e != nil {
		return nil, e
	}
	if body.Revision != g.number("revision") {
		return nil, failure(409, "配置已更新，请刷新后重试")
	}
	c, e := homeConfig(ctx, tx, r.PathValue("id"), body.Revision)
	if e != nil {
		return nil, e
	}
	users, e := query(ctx, tx, `SELECT u.id,u.name FROM users u LEFT JOIN user_archives a ON a.user_id=u.id WHERE u.enabled=1 AND a.user_id IS NULL ORDER BY u.id`)
	if e != nil {
		return nil, e
	}
	nodes := []hg.Node{}
	for _, u := range users {
		nodes = append(nodes, hg.Node{ID: "user-" + strconv.FormatInt(u.number("id"), 10), Name: u.text("name")})
	}
	result, e := hg.PreviewRules(c, body.Source, body.Scope, body.Subject, nodes)
	if e != nil {
		return nil, failure(422, e.Error())
	}
	return result, nil
}
func (app *panel) homeRollback(_ http.ResponseWriter, r *http.Request) (any, error) {
	var b struct {
		Revision int64
		Current  int64
	}
	if e := decode(r, &b); e != nil {
		return nil, e
	}
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		c, e := homeConfig(r.Context(), tx, r.PathValue("id"), b.Revision)
		if errors.Is(e, sql.ErrNoRows) {
			return failure(404, "历史配置不存在")
		}
		if e != nil {
			return e
		}
		c.Revision = b.Current
		return app.saveHomeConfig(r.Context(), tx, r.PathValue("id"), c)
	})
	return row{"state": "pending"}, e
}
func (app *panel) homeDevices(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	g, e := homeRow(ctx, tx, r.PathValue("id"))
	if e != nil {
		return nil, e
	}
	rows, e := query(ctx, tx, "SELECT body,upload,download FROM home_devices WHERE gateway_id=? ORDER BY last_seen DESC,mac", r.PathValue("id"))
	if e != nil {
		return nil, e
	}
	out := []hg.Device{}
	for _, v := range rows {
		var d hg.Device
		if e = json.Unmarshal([]byte(v.text("body")), &d); e != nil {
			return nil, e
		}
		d.Upload = v.number("upload")
		d.Download = v.number("download")
		d.Online = d.Online && g.number("last_seen") > unixNow()-30 && g.number("enabled") == 1
		out = append(out, d)
	}
	return out, nil
}
func (app *panel) homeEvents(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		return nil, failure(422, "页码无效")
	}
	where := "gateway_id=?"
	args := []any{r.PathValue("id")}
	if m := r.URL.Query().Get("mac"); m != "" {
		if _, e := hg.MAC(m); e != nil {
			return nil, failure(422, e.Error())
		}
		where += " AND mac=?"
		args = append(args, m)
	}
	n, e := one(ctx, tx, "SELECT COUNT(*) n FROM home_events WHERE "+where, args...)
	if e != nil {
		return nil, e
	}
	args = append(args, (page-1)*50)
	rows, e := query(ctx, tx, "SELECT body FROM home_events WHERE "+where+" ORDER BY ts DESC,event_id DESC LIMIT 50 OFFSET ?", args...)
	if e != nil {
		return nil, e
	}
	out := []json.RawMessage{}
	for _, v := range rows {
		out = append(out, json.RawMessage(v.text("body")))
	}
	return row{"total": n["n"], "page": page, "items": out}, nil
}
func (app *panel) homeTask(_ http.ResponseWriter, r *http.Request) (any, error) {
	var b struct {
		Kind    string
		Payload map[string]string
	}
	if e := decode(r, &b); e != nil {
		return nil, e
	}
	switch b.Kind {
	case "proxy.restart", "gateway.reboot", "diagnostics", "backup.create", "backup.restore", "wifi.set", "wifi.restart", "ap.reboot":
	default:
		return nil, failure(422, "此硬件或程序不支持该操作")
	}
	if len(b.Payload) > 1 {
		return nil, failure(422, "任务参数无效")
	}
	for k, v := range b.Payload {
		if b.Kind == "wifi.set" && k == "settings" {
			var settings homeap.Settings
			if len(v) > 4096 || json.Unmarshal([]byte(v), &settings) != nil {
				return nil, failure(422, "无线设置格式无效")
			}
			if e := settings.Validate(); e != nil {
				return nil, failure(422, e.Error())
			}
			continue
		}
		if b.Kind != "backup.restore" || k != "backup_id" || !hex32.MatchString(v) {
			return nil, failure(422, "任务参数无效")
		}
	}
	if b.Kind == "wifi.set" && b.Payload["settings"] == "" {
		return nil, failure(422, "请填写无线设置")
	}
	if b.Kind == "backup.restore" && b.Payload["backup_id"] == "" {
		return nil, failure(422, "请选择备份")
	}
	id := randomHex(16)
	created := unixNow()
	raw, _ := json.Marshal(b.Payload)
	e := app.write(r.Context(), func(tx *sql.Tx) error {
		g, e := homeRow(r.Context(), tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if g.number("enabled") != 1 || g.number("last_seen") < created-30 {
			return failure(409, "家庭网关离线，操作未排队")
		}
		if b.Kind == "wifi.set" || b.Kind == "wifi.restart" || b.Kind == "ap.reboot" {
			var caps hg.Capabilities
			if json.Unmarshal([]byte(g.text("capabilities")), &caps) != nil || !caps.WiFi || (b.Kind == "ap.reboot" && !caps.APReboot) {
				return failure(422, "已接入硬件不支持该操作")
			}
		}
		if b.Kind == "backup.create" {
			b.Payload = map[string]string{"revision": strconv.FormatInt(g.number("revision"), 10)}
			raw, _ = json.Marshal(b.Payload)
		}
		if b.Kind == "backup.restore" {
			backup, e := one(r.Context(), tx, "SELECT payload FROM home_tasks WHERE id=? AND gateway_id=? AND kind='backup.create' AND state='success'", b.Payload["backup_id"], g["id"])
			if errors.Is(e, sql.ErrNoRows) {
				return failure(422, "可用备份不存在")
			}
			if e != nil {
				return e
			}
			var info map[string]string
			if json.Unmarshal([]byte(backup.text("payload")), &info) != nil {
				return failure(422, "备份记录无效")
			}
			revision, e := strconv.ParseInt(info["revision"], 10, 64)
			if e != nil {
				return failure(422, "备份配置版本无效")
			}
			c, e := homeConfig(r.Context(), tx, g.text("id"), revision)
			if e != nil {
				return e
			}
			c.Revision = g.number("revision")
			if e = app.saveHomeConfig(r.Context(), tx, g.text("id"), c); e != nil {
				return e
			}
		}
		v, e := one(r.Context(), tx, "SELECT COUNT(*) n FROM home_tasks WHERE gateway_id=? AND state IN ('pending','running')", r.PathValue("id"))
		if e != nil {
			return e
		}
		if v.number("n") >= 8 {
			return failure(409, "请等待当前任务完成")
		}
		if _, e = tx.Exec("INSERT INTO home_tasks(id,gateway_id,kind,created,expires,state,payload) VALUES(?,?,?,?,?,'pending',?)", id, r.PathValue("id"), b.Kind, created, created+60, string(raw)); e != nil {
			return e
		}
		return homeAudit(tx, r.PathValue("id"), b.Kind, id)
	})
	return row{"id": id, "state": "pending"}, e
}
func (app *panel) homeSync(_ http.ResponseWriter, r *http.Request) (any, error) {
	var s hg.Sync
	if e := decode(r, &s); e != nil {
		return nil, e
	}
	if len(s.Boot) < 16 || len(s.Boot) > 64 || s.Sequence < 1 || s.Sequence > 1<<50 || len(s.Devices) > 256 || len(s.Events) > 256 || len(s.Results) > 64 || len(s.Version) > 64 || len(s.Error) > 1024 {
		return nil, failure(422, "网关上报超出限制")
	}
	if s.Status == nil {
		s.Status = map[string]any{}
	}
	status, e := json.Marshal(s.Status)
	if e != nil || len(status) > 32768 {
		return nil, failure(422, "网关状态超出限制")
	}
	caps, _ := json.Marshal(s.Capabilities)
	for _, d := range s.Devices {
		if m, e := hg.MAC(d.MAC); e != nil || m != d.MAC || d.Upload < 0 || d.Download < 0 || len(d.Name) > 192 || len(d.IP) > 64 {
			return nil, failure(422, "设备上报无效")
		}
	}
	for _, v := range s.Events {
		if v.ID == "" || len(v.ID) > 128 || len(v.Target) > 253 || len(v.Rule) > 256 || len(v.Action) > 64 || v.TS > unixNow()+300 {
			return nil, failure(422, "连接记录无效")
		}
	}
	var reply hg.Reply
	e = app.write(r.Context(), func(tx *sql.Tx) error {
		g, e := homeRow(r.Context(), tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if e = homeAuth(g, r); e != nil {
			return e
		}
		previous, e := one(r.Context(), tx, "SELECT sequence FROM home_boots WHERE gateway_id=? AND boot=?", g["id"], s.Boot)
		fresh := errors.Is(e, sql.ErrNoRows)
		if e != nil && !fresh {
			return e
		}
		if fresh && s.Sequence != 1 {
			return failure(409, "请从新会话第一帧开始上报")
		}
		if fresh || s.Sequence > previous.number("sequence") {
			if _, e = tx.Exec("INSERT INTO home_boots VALUES(?,?,?) ON CONFLICT(gateway_id,boot) DO UPDATE SET sequence=excluded.sequence", g["id"], s.Boot, s.Sequence); e != nil {
				return e
			}
			if _, e = tx.Exec("UPDATE home_gateways SET last_seen=?,version=?,applied=?,error=?,status=?,capabilities=? WHERE id=?", unixNow(), s.Version, s.Applied, s.Error, string(status), string(caps), g["id"]); e != nil {
				return e
			}
			for _, d := range s.Devices {
				raw, _ := json.Marshal(d)
				if _, e = tx.Exec(`INSERT INTO home_devices VALUES(?,?,?,?,?,?) ON CONFLICT(gateway_id,mac) DO UPDATE SET body=excluded.body,last_seen=MAX(last_seen,excluded.last_seen),upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download)`, g["id"], d.MAC, string(raw), d.LastSeen, d.Upload, d.Download); e != nil {
					return e
				}
			}
			for _, v := range s.Events {
				raw, _ := json.Marshal(v)
				if _, e = tx.Exec("INSERT OR IGNORE INTO home_events VALUES(?,?,?,?,?,?,?,?)", g["id"], v.ID, v.TS, v.MAC, v.Target, v.Rule, v.Action, string(raw)); e != nil {
					return e
				}
			}
			for _, v := range s.Results {
				if v.State != "success" && v.State != "failed" && v.State != "expired" && v.State != "running" {
					return failure(422, "任务状态无效")
				}
				if len(v.Detail) > 4096 {
					return failure(422, "任务结果过长")
				}
				finished := int64(0)
				if v.State != "running" {
					finished = unixNow()
				}
				if _, e = tx.Exec("UPDATE home_tasks SET state=?,detail=?,finished=? WHERE id=? AND gateway_id=? AND state IN ('pending','running')", v.State, v.Detail, finished, v.ID, g["id"]); e != nil {
					return e
				}
				if finished > 0 {
					if _, e = tx.Exec("UPDATE home_tasks SET payload='{}' WHERE id=? AND gateway_id=? AND kind='wifi.set'", v.ID, g["id"]); e != nil {
						return e
					}
				}
			}
		}
		reply.Accepted = s.Sequence
		candidate, e := homeConfig(r.Context(), tx, g.text("id"), g.number("revision"))
		if e != nil {
			return e
		}
		if e = app.homeNodes(r.Context(), tx, &candidate, true); e != nil {
			var invalid httpError
			if !errors.As(e, &invalid) || invalid.status != 422 {
				return e
			}
			// A removed proxy must not roll back telemetry and task acknowledgements.
			// Keep the last valid local policy; never silently switch to direct.
			reply.ConfigError = invalid.message
		} else {
			reply.Config = &candidate
		}
		if _, e = tx.Exec("UPDATE home_tasks SET state='expired',detail='任务已过期',finished=? WHERE gateway_id=? AND state='pending' AND expires<?", unixNow(), g["id"], unixNow()); e != nil {
			return e
		}
		tasks, e := query(r.Context(), tx, "SELECT id,kind,created,expires,payload FROM home_tasks WHERE gateway_id=? AND state IN ('pending','running') AND expires>=? ORDER BY created,id LIMIT 8", g["id"], unixNow())
		if e != nil {
			return e
		}
		reply.Tasks = []hg.Task{}
		for _, v := range tasks {
			task := hg.Task{ID: v.text("id"), Kind: v.text("kind"), Created: v.number("created"), Expires: v.number("expires")}
			json.Unmarshal([]byte(v.text("payload")), &task.Payload)
			reply.Tasks = append(reply.Tasks, task)
		}
		_, e = tx.Exec("DELETE FROM home_events WHERE gateway_id=? AND ts<?", g["id"], time.Now().Add(-31*24*time.Hour).Unix())
		return e
	})
	return reply, e
}
