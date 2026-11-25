package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const maxSafeInt = 1<<53 - 1
const limitPrefix = "treeroot-limit-"

// User creation and Xray registration are shared by ordinary users and the
// dedicated identity belonging to each home gateway.
func insertProxyUser(ctx context.Context, tx *sql.Tx, name string) (row, error) {
	result, e := tx.Exec("INSERT INTO users(name,client_uuid,email,token,created) VALUES(?,?,?,?,?)", name, uuid.NewString(), "u-"+randomHex(16)+"@treeroot.local", randomToken(), unixNow())
	if e != nil {
		return nil, e
	}
	id, e := result.LastInsertId()
	if e != nil {
		return nil, e
	}
	return one(ctx, tx, "SELECT id,name,enabled,email,client_uuid,token,created,upload,download FROM users WHERE id=?", id)
}

type proxyUserChange struct {
	original       []byte
	before, after  row
	removed, added bool
}

func (app *panel) registerProxyUser(ctx context.Context, tx *sql.Tx, after row, change *proxyUserChange) error {
	change.after = after
	var e error
	change.original, e = os.ReadFile(app.config.XrayConfig)
	if e != nil {
		return e
	}
	users, e := query(ctx, tx, "SELECT id,email,client_uuid,enabled FROM users ORDER BY id")
	if e != nil {
		return e
	}
	if e = app.applyXray(change.original, users, nil, after, &change.removed, &change.added); e != nil {
		logError("apply gateway proxy user", e)
		return failure(503, "代理服务更新失败，改动已回滚")
	}
	return nil
}
func (app *panel) rollbackProxyUser(change *proxyUserChange) {
	if change.original == nil {
		return
	}
	if change.added {
		_, e := app.xray("rmu", "-tag=websocket-in", change.after.text("email"))
		logError("revert Xray addition", e)
	}
	if change.removed {
		logError("restore Xray user", app.addXrayUser(change.original, change.before))
	}
	var cfg row
	if json.Unmarshal(change.original, &cfg) == nil {
		logError("restore Xray routing", app.syncRouting(cfg))
	}
	logError("restore Xray config", replaceConfig(app.config.XrayConfig, change.original))
}

func idParam(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, failure(422, "请求参数无效")
	}
	return id, nil
}

func (app *panel) routesUsers() {
	app.route("GET /api/users", true, app.report(app.users))
	app.route("POST /api/users", true, app.changeUser)
	app.route("PATCH /api/users/{id}", true, app.changeUser)
	app.route("POST /api/users/{id}/reset", true, app.changeUser)
	app.route("DELETE /api/users/{id}", true, app.changeUser)
	app.route("POST /api/users/{id}/restore", true, app.changeUser)
	app.route("GET /api/users/{id}/limits", true, app.report(func(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
		id, err := idParam(r)
		if err != nil {
			return nil, err
		}
		return readLimits(ctx, tx, id)
	}))
	app.route("PATCH /api/users/{id}/limits", true, app.updateLimits)
	app.route("GET /api/overview", true, app.report(app.overview))
	app.route("GET /api/rates", true, func(_ http.ResponseWriter, r *http.Request) (any, error) {
		id, ok := queryInt(r.URL.Query(), "user_id", 0)
		if !ok {
			return nil, failure(422, "查询参数无效")
		}
		return app.rates(id), nil
	})
}

func (app *panel) publicUser(user row) row {
	result := user.fields("id", "name", "enabled", "created", "upload", "download")
	base := app.baseURL + "/sub/" + user.text("token")
	result["clash_url"], result["shadowrocket_url"] = base+"/clash.yaml", base+"/shadowrocket"
	return result
}

const limitsSelect = `COALESCE(l.total_bytes,0) total_bytes,COALESCE(l.daily_bytes,0) daily_bytes,
	COALESCE(l.speed_bps,0) speed_bps,COALESCE(t.total_bytes,u.upload+u.download) total_used,
	COALESCE(d.bytes,0) daily_used`
const limitsJoin = ` LEFT JOIN user_limits l ON l.user_id=u.id
	LEFT JOIN limit_usage t ON t.user_id=u.id LEFT JOIN limit_daily d ON d.user_id=u.id AND d.day=?`

func shanghaiDay() string { return time.Now().UTC().Add(8 * time.Hour).Format(time.DateOnly) }

func publicLimits(item row) row {
	result := item.fields("total_bytes", "daily_bytes", "speed_bps", "total_used", "daily_used")
	result["blocked"] = nil
	if item.number("total_bytes") > 0 && item.number("total_used") >= item.number("total_bytes") {
		result["blocked"] = "total"
	} else if item.number("daily_bytes") > 0 && item.number("daily_used") >= item.number("daily_bytes") {
		result["blocked"] = "daily"
	}
	return result
}

func readLimits(ctx context.Context, db querier, id int64) (row, error) {
	item, err := one(ctx, db, `SELECT `+limitsSelect+`, (SELECT CAST(value AS REAL) FROM state WHERE key='limits_heartbeat') heartbeat FROM users u`+limitsJoin+` WHERE u.id=? AND NOT EXISTS(SELECT 1 FROM user_archives a WHERE a.user_id=u.id)`, shanghaiDay(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, failure(404, "用户不存在")
	}
	if err != nil {
		return nil, err
	}
	result := publicLimits(item)
	sample, _ := item["heartbeat"].(float64)
	age := float64(time.Now().UnixMilli())/1000 - sample
	result["ready"] = age >= 0 && age < 5
	return result, nil
}

func (app *panel) updateLimits(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := idParam(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Total int64 `json:"total_bytes"`
		Daily int64 `json:"daily_bytes"`
		Speed int64 `json:"speed_bps"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.Total < 0 || body.Total > maxSafeInt || body.Daily < 0 || body.Daily > maxSafeInt || body.Speed < 0 || body.Speed > 1e12 {
		return nil, failure(422, "额度或速率无效")
	}
	var result row
	ctx := r.Context()
	err = app.write(ctx, func(tx *sql.Tx) error {
		if _, err := readLimits(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_limits VALUES(?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET total_bytes=excluded.total_bytes,daily_bytes=excluded.daily_bytes,speed_bps=excluded.speed_bps,updated=excluded.updated`, id, body.Total, body.Daily, body.Speed, unixNow()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO audit(ts,action,user_id) VALUES(?,'update_limits',?)`, unixNow(), id); err != nil {
			return err
		}
		var err error
		result, err = readLimits(ctx, tx, id)
		return err
	})
	return result, err
}

func (app *panel) users(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	deleted := false
	if value := r.URL.Query().Get("deleted"); value != "" {
		var err error
		deleted, err = strconv.ParseBool(value)
		if err != nil {
			return nil, failure(422, "查询参数无效")
		}
	}
	rows, err := query(ctx, tx, `SELECT u.id,u.name,u.enabled,u.created,u.upload,u.download,u.token,(SELECT gateway_id FROM home_proxy_users WHERE user_id=u.id) home_gateway_id,a.deleted_at,COALESCE(s.domains,0) domains,COALESCE(s.connections,0) connections,s.last_used,`+limitsSelect+`
		FROM users u LEFT JOIN user_archives a ON a.user_id=u.id`+limitsJoin+` LEFT JOIN (SELECT user_id,COUNT(*) domains,SUM(connections) connections,MAX(last_seen) last_used FROM domains GROUP BY user_id) s ON s.user_id=u.id WHERE (a.user_id IS NOT NULL)=? ORDER BY u.id`, shanghaiDay(), deleted)
	if err != nil {
		return nil, err
	}
	for i, item := range rows {
		user := app.publicUser(item)
		user["deleted"], user["deleted_at"] = deleted, item["deleted_at"]
		user["home_gateway_id"] = item["home_gateway_id"]
		user["domains"], user["connections"], user["last_used"], user["limits"] = item["domains"], item["connections"], item["last_used"], publicLimits(item)
		rows[i] = user
	}
	return rows, nil
}

func (app *panel) changeUser(_ http.ResponseWriter, r *http.Request) (any, error) {
	var id int64
	var err error
	create := r.PathValue("id") == ""
	remove := r.Method == "DELETE"
	restore := strings.HasSuffix(r.URL.Path, "/restore")
	reset := !create && r.Method == "POST" && !restore
	if !create {
		id, err = idParam(r)
		if err != nil {
			return nil, err
		}
	}
	var body struct {
		Name    *string
		Enabled *bool
	}
	if !reset && !remove && !restore {
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		if body.Name != nil {
			*body.Name = strings.TrimSpace(*body.Name)
			if !validName(*body.Name) {
				return nil, failure(422, "请输入用户名称")
			}
		}
		if create && body.Name == nil {
			return nil, failure(422, "请输入用户名称")
		}
	}
	var before, after row
	var original []byte
	var removed, added bool
	ctx := r.Context()
	err = app.submit(writeJob{ctx: ctx, run: func(tx *sql.Tx) error {
		action := "update_user"
		if create {
			action = "create_user"
			created, e := insertProxyUser(ctx, tx, *body.Name)
			if e != nil {
				return e
			}
			id = created.number("id")
		} else {
			if remove || reset || body.Enabled != nil && !*body.Enabled {
				owned, e := one(ctx, tx, "SELECT gateway_id FROM home_proxy_users WHERE user_id=?", id)
				if e == nil && owned.text("gateway_id") != "" {
					return failure(409, "这是家庭网关的专属统计账号，不能删除、停用或重置")
				}
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
			}
			before, err = one(ctx, tx, "SELECT enabled,email,client_uuid,EXISTS(SELECT 1 FROM user_archives a WHERE a.user_id=users.id) archived FROM users WHERE id=?", id)
			if errors.Is(err, sql.ErrNoRows) {
				return failure(404, "用户不存在")
			}
			if err != nil {
				return err
			}
			if before.number("archived") == 1 && !restore {
				return failure(404, "用户已删除")
			}
			if restore && before.number("archived") == 0 {
				return failure(404, "已删除用户不存在")
			}
			if remove {
				action = "delete_user"
				if _, err = tx.Exec("INSERT INTO user_archives(user_id,deleted_at) VALUES(?,?)", id, unixNow()); err != nil {
					return err
				}
				_, err = tx.Exec("UPDATE users SET enabled=0 WHERE id=?", id)
			} else if restore {
				action = "restore_user"
				if _, err = tx.Exec("DELETE FROM user_archives WHERE user_id=?", id); err != nil {
					return err
				}
				_, err = tx.Exec("UPDATE users SET enabled=0 WHERE id=?", id)
			} else if reset {
				action = "reset_subscription"
				_, err = tx.Exec("UPDATE users SET client_uuid=?,token=? WHERE id=?", uuid.NewString(), randomToken(), id)
			} else {
				_, err = tx.Exec("UPDATE users SET name=COALESCE(?,name),enabled=COALESCE(?,enabled) WHERE id=?", body.Name, body.Enabled, id)
			}
			if err != nil {
				return err
			}
		}
		after, err = one(ctx, tx, "SELECT id,name,enabled,email,client_uuid,token,created,upload,download FROM users WHERE id=?", id)
		if err != nil {
			return err
		}
		if create || before["enabled"] != after["enabled"] || reset {
			original, err = os.ReadFile(app.config.XrayConfig)
			if err != nil {
				return err
			}
			users, err := query(ctx, tx, "SELECT id,email,client_uuid,enabled FROM users ORDER BY id")
			if err != nil {
				return err
			}
			if err := app.applyXray(original, users, before, after, &removed, &added); err != nil {
				logError("apply Xray user change", err)
				return failure(503, "代理服务更新失败，改动已回滚")
			}
		}
		_, err = tx.Exec("INSERT INTO audit(ts,action,user_id) VALUES(?,?,?)", unixNow(), action, id)
		return err
	}, rollback: func() {
		if original == nil {
			return
		}
		if added {
			_, err := app.xray("rmu", "-tag=websocket-in", after.text("email"))
			logError("revert Xray addition", err)
		}
		if removed {
			logError("restore Xray user", app.addXrayUser(original, before))
		}
		var cfg row
		if json.Unmarshal(original, &cfg) == nil {
			logError("restore Xray routing", app.syncRouting(cfg))
		}
		logError("restore Xray config", replaceConfig(app.config.XrayConfig, original))
	}})
	return app.publicUser(after), err
}

func command(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func (app *panel) xray(action string, args ...string) ([]byte, error) {
	output, err := command(12*time.Second, app.config.Xray, append([]string{"api", action, "--server=" + app.config.XrayAPI}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("Xray %s: %w", action, err)
	}
	if action == "adu" && !strings.Contains(string(output), "Added 1 user(s)") || action == "rmu" && !strings.Contains(string(output), "Removed 1 user(s)") {
		return nil, fmt.Errorf("Xray rejected %s", action)
	}
	return output, nil
}

func vmessClient(user row) row {
	return row{"id": user["client_uuid"], "email": user["email"], "level": 0, "alterId": 0}
}

func websocketInbound(cfg map[string]any) row {
	list, _ := cfg["inbounds"].([]any)
	for _, entry := range list {
		if item, ok := entry.(map[string]any); ok && item["tag"] == "websocket-in" {
			return item
		}
	}
	return nil
}

func (app *panel) xrayFile(value any, run func(string) error) error {
	file, err := os.CreateTemp(app.config.DataDir, "xray-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return run(file.Name())
}

func (app *panel) addXrayUser(raw []byte, user row) error {
	var cfg row
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	inbound := websocketInbound(cfg)
	settings, ok := inbound["settings"].(map[string]any)
	if !ok {
		return errors.New("Xray websocket inbound not found")
	}
	settings["clients"] = []row{vmessClient(user)}
	return app.xrayFile(row{"inbounds": []row{inbound}}, func(path string) error { _, err := app.xray("adu", path); return err })
}

func appendRoutes(cfg row, users []row) {
	outbounds := cfg["outbounds"].([]any)
	hasLimits := false
	for _, value := range outbounds {
		if strings.HasPrefix(row(value.(map[string]any)).text("tag"), limitPrefix) {
			hasLimits = true
			break
		}
	}
	if !hasLimits {
		return
	}
	routing := cfg["routing"].(map[string]any)
	keep := func(list []any, key string) []any {
		result := make([]any, 0, len(list))
		for _, value := range list {
			if !strings.HasPrefix(row(value.(map[string]any)).text(key), limitPrefix) {
				result = append(result, value)
			}
		}
		return result
	}
	outbounds = keep(outbounds, "tag")
	rules := keep(routing["rules"].([]any), "ruleTag")
	for _, user := range users {
		tag := fmt.Sprintf("%s%d-%s", limitPrefix, user.number("id"), digest(user.text("client_uuid"))[:10])
		outbounds = append(outbounds, map[string]any{"tag": tag, "protocol": "socks", "settings": row{"servers": []row{{"address": "127.0.0.1", "port": 10002, "users": []row{{"user": user["email"], "pass": user["client_uuid"]}}}}}})
		rules = append(rules, row{"type": "field", "ruleTag": tag, "inboundTag": []string{"websocket-in"}, "user": []string{user.text("email")}, "outboundTag": tag})
	}
	cfg["outbounds"], routing["rules"] = outbounds, append(rules, row{"type": "field", "ruleTag": limitPrefix + "fallback", "inboundTag": []string{"websocket-in"}, "outboundTag": "block"})
}

func (app *panel) syncRouting(cfg row) error {
	wanted := map[string]map[string]any{}
	for _, value := range cfg["outbounds"].([]any) {
		out := value.(map[string]any)
		tag := row(out).text("tag")
		if strings.HasPrefix(tag, limitPrefix) {
			wanted[tag] = out
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	before, err := app.xray("lso")
	if err != nil {
		return err
	}
	var live struct{ Outbounds []struct{ Tag string } }
	if err := json.Unmarshal(before, &live); err != nil {
		return err
	}
	var remove []string
	for _, out := range live.Outbounds {
		if strings.HasPrefix(out.Tag, limitPrefix) {
			if _, ok := wanted[out.Tag]; ok {
				delete(wanted, out.Tag)
			} else {
				remove = append(remove, out.Tag)
			}
		}
	}
	additions := make([]map[string]any, 0, len(wanted))
	for _, item := range wanted {
		additions = append(additions, item)
	}
	if err := app.xrayFile(row{"outbounds": additions, "routing": cfg["routing"]}, func(path string) error {
		if len(additions) > 0 {
			if _, err := app.xray("ado", path); err != nil {
				return err
			}
		}
		_, err := app.xray("adrules", path)
		return err
	}); err != nil {
		return err
	}
	for _, tag := range remove {
		if _, err := app.xray("rmo", tag); err != nil {
			return err
		}
	}
	return nil
}

func (app *panel) applyXray(raw []byte, users []row, before, after row, removed, added *bool) error {
	var cfg row
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	inbound := websocketInbound(cfg)
	settings, ok := inbound["settings"].(map[string]any)
	if !ok {
		return errors.New("Xray websocket inbound not found")
	}
	clients := []row{}
	for _, user := range users {
		if user.number("enabled") == 1 {
			clients = append(clients, vmessClient(user))
		}
	}
	settings["clients"] = clients
	appendRoutes(cfg, users)
	productionLog := cfg["log"]
	cfg["log"] = row{"access": "none", "loglevel": "none"}
	if err := app.xrayFile(cfg, func(path string) error {
		_, err := command(10*time.Second, app.config.Xray, "run", "-test", "-config", path)
		return err
	}); err != nil {
		return err
	}
	cfg["log"] = productionLog
	next, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if before.number("enabled") == 1 {
		if _, err := app.xray("rmu", "-tag=websocket-in", before.text("email")); err != nil {
			return err
		}
		*removed = true
	}
	if err := app.syncRouting(cfg); err != nil {
		return err
	}
	if after.number("enabled") == 1 {
		if err := app.addXrayUser(raw, after); err != nil {
			return err
		}
		*added = true
	}
	return replaceConfig(app.config.XrayConfig, next)
}

func replaceConfig(path string, value []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "xray-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chown(-1, int(info.Sys().(*syscall.Stat_t).Gid)); err != nil {
		return err
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := file.Write(value); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
