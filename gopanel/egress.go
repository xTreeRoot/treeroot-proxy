package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"
)

var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var domainPattern = regexp.MustCompile(`^[a-zA-Z0-9.:%_-]{0,253}$`)
var methodPattern = regexp.MustCompile(`^[A-Z]{0,16}$`)
var versionPattern = regexp.MustCompile(`^v[0-9]{4}\.[0-9]{2}\.[0-9]{2}\.[0-9]+$`)
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var ipv4Pattern = regexp.MustCompile(`(?:[0-9]{1,3}\.){3}[0-9]{1,3}`)

func (app *panel) routesEgress() {
	app.route("GET /api/egress", true, app.report(app.devices))
	app.route("POST /api/egress", true, app.createDevice)
	app.route("PATCH /api/egress/{id}", true, app.deviceChange)
	app.route("DELETE /api/egress/{id}", true, app.deviceChange)
	app.route("POST /api/egress/{id}/restore", true, app.deviceChange)
	app.route("GET /api/egress/{id}/access-code", true, app.report(func(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
		device, err := deviceRow(ctx, tx, r.PathValue("id"))
		return row{"code": device["access_code"]}, err
	}))
	app.route("POST /api/egress/{id}/setup", true, app.setup)
	app.route("POST /api/egress/{id}/pair", false, app.pair)
	app.route("POST /api/egress/{id}/heartbeat", false, app.heartbeat)
	app.route("POST /api/egress/{id}/test", true, app.testEgress)
	app.route("GET /api/egress/version", true, func(http.ResponseWriter, *http.Request) (any, error) { return row{"version": app.version}, nil })
	app.route("GET /api/egress/overview", true, app.report(app.egressOverview))
	app.route("GET /api/egress/rates", true, app.report(app.egressRates))
	app.route("GET /api/egress/records", true, app.report(app.egressRecords))
	app.route("GET /api/egress/blacklist", true, app.report(func(ctx context.Context, tx *sql.Tx, _ *http.Request) (any, error) { return blacklistRows(ctx, tx) }))
	app.route("POST /api/egress/blacklist", true, app.blacklistChange)
	app.route("PATCH /api/egress/blacklist/{id}", true, app.blacklistChange)
	app.route("DELETE /api/egress/blacklist/{id}", true, app.blacklistChange)
}

const deviceSummaryColumns = "id,name,created,last_seen,last_ip,last_test,driver_version,browser,enabled,public_key"

func deviceRow(ctx context.Context, db querier, id string) (row, error) {
	item, err := one(ctx, db, "SELECT "+deviceSummaryColumns+",port,access_code,proxy_password,pair_hash,pair_expires,agent_hash FROM egress_devices WHERE id=? AND deleted=0", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, failure(404, "出口设备不存在")
	}
	return item, err
}

func online(item row, now int64) bool {
	return item.number("deleted") == 0 && item.number("enabled") == 1 && item.text("public_key") != "" && item.number("last_seen") > now-65
}

func deviceSummary(item row) row {
	result := item.fields("id", "name", "created", "last_seen", "last_ip", "last_test", "driver_version")
	result["browser"] = json.RawMessage(item.text("browser"))
	result["enabled"], result["online"], result["paired"] = item.number("enabled") == 1, online(item, unixNow()), item.text("public_key") != ""
	return result
}

func (app *panel) publishRegistry() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := query(ctx, app.database, `SELECT id,name,enabled,access_code,last_seen,browser,port,proxy_password,public_key FROM egress_devices WHERE deleted=0`)
	if err != nil {
		return err
	}
	for idx, item := range rows {
		rows[idx] = row{"id": item["id"], "name": item["name"], "enabled": item.number("enabled") == 1, "accessHash": digest(item.text("access_code")),
			"lastSeen": item["last_seen"], "browser": json.RawMessage(item.text("browser")), "proxy": row{"server": fmt.Sprintf("http://127.0.0.1:%d", item.number("port")), "username": item["id"], "password": item["proxy_password"]}, "publicKey": item["public_key"], "port": item["port"]}
	}
	if err := os.MkdirAll(app.config.EgressDir, 0750); err != nil {
		return err
	}
	file, err := os.CreateTemp(app.config.EgressDir, "registry-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0640); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(row{"version": 1, "devices": rows}); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(app.config.EgressDir, "registry.json"))
}

func (app *panel) egressWrite(ctx context.Context, run func(*sql.Tx) error, after func()) error {
	var publishErr error
	err := app.submit(writeJob{ctx: ctx, run: run, after: func() {
		if after != nil {
			after()
		}
		publishErr = app.publishRegistry()
	}})
	if err != nil {
		return err
	}
	return publishErr
}

func deviceEvent(tx *sql.Tx, device row, action, outcome, detail string) error {
	_, err := tx.Exec(`INSERT INTO egress_events(event_key,device_id,ts,kind,action,outcome,exit_ip,detail) VALUES(?,?,?,'device',?,?,?,?)`, randomHex(16), device["id"], unixNow(), action, outcome, device.text("last_ip"), detail)
	return err
}

func syncTotals(tx *sql.Tx, id string) error {
	where, args := "", []any{}
	if id != "" {
		where = " AND d.id=?"
		args = append(args, id)
	}
	_, err := tx.Exec(`INSERT INTO egress_totals(device_id,upload,download,connections,last_event_id,since)
	SELECT d.id,SUM(e.upload),SUM(e.download),COUNT(*),MAX(e.id),MIN(e.ts)
	FROM egress_devices d LEFT JOIN egress_totals t ON t.device_id=d.id CROSS JOIN egress_events e
	WHERE e.device_id=d.id AND e.id>COALESCE(t.last_event_id,0) AND e.kind='request'`+where+` GROUP BY d.id
	ON CONFLICT(device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download,
	connections=connections+excluded.connections,last_event_id=excluded.last_event_id,since=MIN(since,excluded.since)`, args...)
	return err
}

func disconnectEvents(tx *sql.Tx, now int64, id string) error {
	where, args := "", []any{now - 65}
	if id != "" {
		where = " AND id=?"
		args = append(args, id)
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO egress_events(event_key,device_id,ts,kind,action,outcome,exit_ip)
	SELECT 'offline:'||last_seen,id,last_seen+65,'device','disconnected','success',COALESCE(last_ip,'')
	FROM egress_devices WHERE enabled=1 AND deleted=0 AND public_key IS NOT NULL AND last_seen<=?`+where, args...)
	return err
}

func (app *panel) devices(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	var deleted bool
	if value := r.URL.Query().Get("deleted"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return nil, failure(422, "查询参数无效")
		}
		deleted, _ = strconv.ParseBool(value)
	}
	rows, err := query(ctx, tx, "SELECT "+deviceSummaryColumns+" FROM egress_devices WHERE deleted=? ORDER BY created,id", deleted)
	if err != nil {
		return nil, err
	}
	for idx, item := range rows {
		rows[idx] = deviceSummary(item)
	}
	return rows, nil
}

func (app *panel) createDevice(_ http.ResponseWriter, r *http.Request) (any, error) {
	var body struct {
		Name    string
		Browser map[string]json.RawMessage
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	body.Name = strings.TrimSpace(body.Name)
	if !validName(body.Name) {
		return nil, failure(422, "请输入电脑名称")
	}
	browser := row{"userAgent": "", "platform": "", "language": "zh-CN", "timezone": "Asia/Shanghai", "platformName": "", "platformVersion": "", "architecture": "", "bitness": "", "width": 1280, "height": 900}
	for key, n := range map[string]int{"userAgent": 512, "platform": 64, "language": 35, "timezone": 80, "platformName": 32, "platformVersion": 32, "architecture": 16, "bitness": 8} {
		if raw, ok := body.Browser[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil || utf8.RuneCountInString(value) > n {
				return nil, failure(422, "电脑信息无效")
			}
			browser[key] = value
		}
	}
	for key, bounds := range map[string][2]int{"width": {320, 7680}, "height": {240, 4320}} {
		if raw, ok := body.Browser[key]; ok {
			var value int
			if json.Unmarshal(raw, &value) != nil || value < bounds[0] || value > bounds[1] {
				return nil, failure(422, "电脑信息无效")
			}
			browser[key] = value
		}
	}
	encoded, _ := json.Marshal(browser)
	var result row
	ctx := r.Context()
	err := app.egressWrite(ctx, func(tx *sql.Tx) error {
		rows, err := query(ctx, tx, "SELECT port FROM egress_devices")
		if err != nil {
			return err
		}
		used := map[int64]bool{}
		for _, device := range rows {
			used[device.number("port")] = true
		}
		var port int64
		for candidate := int64(21000); candidate < 22000; candidate++ {
			if used[candidate] {
				continue
			}
			listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", candidate))
			if err == nil {
				listener.Close()
				port = candidate
				break
			}
		}
		if port == 0 {
			return failure(409, "出口连接数量已达上限")
		}
		id := randomHex(16)
		if _, err := tx.Exec(`INSERT INTO egress_devices(id,name,port,created,browser,access_code,proxy_password) VALUES(?,?,?,?,?,?,?)`, id, body.Name, port, unixNow(), string(encoded), randomToken(), randomToken()); err != nil {
			return err
		}
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		result = deviceSummary(item)
		return deviceEvent(tx, item, "created", "success", "")
	}, nil)
	return result, err
}

func (app *panel) clearLive(id string) func() {
	return func() { app.egressMu.Lock(); delete(app.live, id); app.egressMu.Unlock() }
}

func (app *panel) deviceChange(_ http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	var body struct{ Enabled *bool }
	if r.Method == "PATCH" {
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		if body.Enabled == nil {
			return nil, failure(422, "请选择启用状态")
		}
	}
	result := row{"ok": true}
	ctx := r.Context()
	err := app.egressWrite(ctx, func(tx *sql.Tx) error {
		if r.Method == "POST" {
			updated, err := tx.Exec(`UPDATE egress_devices SET deleted=0,enabled=0 WHERE id=? AND deleted=1`, id)
			if err != nil {
				return err
			}
			if n, _ := updated.RowsAffected(); n == 0 {
				return failure(404, "已删除设备不存在")
			}
			item, err := deviceRow(ctx, tx, id)
			if err != nil {
				return err
			}
			return deviceEvent(tx, item, "restored", "success", "")
		}
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Method == "DELETE" {
			if err := deviceEvent(tx, item, "deleted", "success", ""); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE egress_devices SET deleted=1,enabled=0,public_key=NULL,agent_hash=NULL,pair_hash=NULL,pair_expires=NULL,last_seen=NULL,driver_version=NULL WHERE id=?`, id)
			return err
		}
		if (item.number("enabled") == 1) != *body.Enabled {
			action := "disabled"
			if *body.Enabled {
				action = "enabled"
			}
			if err := deviceEvent(tx, item, action, "success", ""); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE egress_devices SET enabled=? WHERE id=?`, *body.Enabled, id); err != nil {
			return err
		}
		item, err = deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		result = deviceSummary(item)
		return nil
	}, app.clearLive(id))
	return result, err
}

func (app *panel) setup(w http.ResponseWriter, r *http.Request) (any, error) {
	if app.config.SSHHost == "" || app.config.KnownHosts == "" {
		return nil, failure(503, "服务器出口连接尚未完成部署")
	}
	id, token, ctx := r.PathValue("id"), randomToken(), r.Context()
	var archive bytes.Buffer
	bundle := zip.NewWriter(&archive)
	add := func(name string, value []byte, mode os.FileMode) error {
		header := zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(mode)
		stream, err := bundle.CreateHeader(&header)
		if err != nil {
			return err
		}
		_, err = stream.Write(value)
		return err
	}
	for _, name := range []string{"connector.py", "connector_version.py", "egress_proxy.py"} {
		data, err := os.ReadFile(filepath.Join(app.root, name))
		if err != nil {
			return nil, err
		}
		if err := add(name, data, 0644); err != nil {
			return nil, err
		}
	}
	setup, _ := json.Marshal(row{"base_url": app.baseURL, "id": id, "token": token, "driver_version": app.version})
	for _, file := range []struct {
		name, text string
		mode       os.FileMode
	}{
		{"连接.command", "#!/bin/bash\ncd \"$(dirname \"$0\")\"\npython3 connector.py install --setup setup.json\nresult=$?\nif [ \"$result\" -ne 0 ]; then echo \"连接失败，请查看上面的提示后重新下载连接包。\"; fi\nread -r -p \"按回车关闭窗口\"\nexit \"$result\"\n", 0755},
		{"连接.cmd", "@echo off\r\ncd /d \"%~dp0\"\r\npy -3 connector.py install --setup setup.json\r\npause\r\n", 0644},
		{"setup.json", string(setup), 0600},
		{"使用说明.txt", "请在本机解压，10 分钟内运行：\nmacOS：双击 连接.command\nWindows：双击 连接.cmd\n需要 Python 3.10+ 和 OpenSSH 客户端。安装后当前用户登录系统时自动连接。\n卸载：python3 connector.py uninstall --id " + id + "\n", 0644},
	} {
		if err := add(file.name, []byte(file.text), file.mode); err != nil {
			return nil, err
		}
	}
	if err := bundle.Close(); err != nil {
		return nil, err
	}
	err := app.write(ctx, func(tx *sql.Tx) error {
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if item.number("enabled") != 1 {
			return failure(409, "请先启用出口设备")
		}
		if _, err := tx.Exec("UPDATE egress_devices SET pair_hash=?,pair_expires=? WHERE id=?", digest(token), unixNow()+600, id); err != nil {
			return err
		}
		return deviceEvent(tx, item, "setup_downloaded", "success", "")
	})
	if err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=TreeRoot-Connector.zip")
	_, err = archive.WriteTo(w)
	return nil, err
}

func (app *panel) pair(_ http.ResponseWriter, r *http.Request) (any, error) {
	var body struct {
		Token     string
		PublicKey string `json:"public_key"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	fields := strings.Fields(body.PublicKey)
	if len(body.Token) < 20 || len(body.Token) > 128 || len(fields) != 2 || fields[0] != "ssh-ed25519" {
		return nil, failure(422, "只接受新生成的 Ed25519 公钥")
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil || len(blob) != 51 || string(blob[:19]) != "\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20" {
		return nil, failure(422, "公钥内容无效")
	}
	id, token, ctx := r.PathValue("id"), randomToken(), r.Context()
	var result row
	err = app.egressWrite(ctx, func(tx *sql.Tx) error {
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if item.number("enabled") != 1 || item.number("pair_expires") < unixNow() || !same(item.text("pair_hash"), digest(body.Token)) {
			return failure(403, "配对包已过期或已使用，请重新下载")
		}
		if _, err := tx.Exec(`UPDATE egress_devices SET public_key=?,agent_hash=?,pair_hash=NULL,pair_expires=NULL,last_seen=NULL,driver_version=NULL WHERE id=?`, strings.Join(fields, " "), digest(token), id); err != nil {
			return err
		}
		if err := deviceEvent(tx, item, "paired", "success", ""); err != nil {
			return err
		}
		result = row{"id": id, "base_url": app.baseURL, "ssh_host": app.config.SSHHost, "ssh_port": 22, "ssh_user": "treeroot-egress", "remote_port": item["port"], "known_hosts": app.config.KnownHosts, "proxy_username": id, "proxy_password": item["proxy_password"], "agent_token": token}
		return nil
	}, app.clearLive(id))
	return result, err
}

type egressTraffic struct {
	Run              string  `json:"run_id"`
	Sampled          float64 `json:"sampled_at"`
	Upload, Download int64
}
type liveSample struct {
	egressTraffic
	received int64
	rates    *[2]float64
}
type connectionRecord struct {
	Key              string `json:"event_key"`
	TS               int64
	Domain           string
	Port             *int
	Method, Outcome  string
	Upload, Download int64
	Duration         int64 `json:"duration_ms"`
	Detail           string
}

func (app *panel) heartbeat(_ http.ResponseWriter, r *http.Request) (any, error) {
	var body struct {
		Records []connectionRecord
		Traffic *egressTraffic
		Version *string `json:"driver_version"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if len(body.Records) > 100 || body.Version != nil && (len(*body.Version) > 32 || !versionPattern.MatchString(*body.Version)) {
		return nil, failure(422, "心跳参数无效")
	}
	t := body.Traffic
	if t != nil && (!hex32.MatchString(t.Run) || t.Sampled < 0 || t.Upload < 0 || t.Upload > maxSafeInt || t.Download < 0 || t.Download > maxSafeInt) {
		return nil, failure(422, "流量样本无效")
	}
	accepted := make([]string, 0, len(body.Records))
	hosts := []string{}
	for _, e := range body.Records {
		if !hex32.MatchString(e.Key) || e.TS < 0 || !domainPattern.MatchString(e.Domain) || e.Port != nil && (*e.Port < 1 || *e.Port > 65535) || !methodPattern.MatchString(e.Method) || e.Outcome != "success" && e.Outcome != "failed" || e.Upload < 0 || e.Upload > maxSafeInt || e.Download < 0 || e.Download > maxSafeInt || e.Duration < 0 || e.Duration > maxSafeInt {
			return nil, failure(422, "连接记录无效")
		}
		switch e.Detail {
		case "", "invalid_request", "target_rejected", "target_blacklisted", "connection_failed", "timeout", "interrupted":
		default:
			return nil, failure(422, "连接记录无效")
		}
		accepted = append(accepted, e.Key)
	}
	id, now, ctx := r.PathValue("id"), unixNow(), r.Context()
	token := digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	err := app.egressWrite(ctx, func(tx *sql.Tx) error {
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if item.number("enabled") != 1 || !same(item.text("agent_hash"), token) {
			return failure(403, "出口连接已停用或凭据无效")
		}
		if item.number("last_seen") <= now-65 {
			if err := disconnectEvents(tx, now, id); err != nil {
				return err
			}
			if err := deviceEvent(tx, item, "connected", "success", ""); err != nil {
				return err
			}
		}
		if len(body.Records) > 0 {
			stmt, err := tx.Prepare(`INSERT OR IGNORE INTO egress_events(event_key,device_id,ts,kind,action,outcome,domain,port,method,upload,download,duration_ms,exit_ip,detail) VALUES(?,?,?,'request','connection',?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, e := range body.Records {
				if e.TS < now-7*86400 || e.TS > now+300 {
					continue
				}
				if _, err := stmt.Exec(e.Key, id, e.TS, e.Outcome, e.Domain, e.Port, e.Method, e.Upload, e.Download, e.Duration, item.text("last_ip"), e.Detail); err != nil {
					return err
				}
			}
			if err := syncTotals(tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE egress_devices SET last_seen=?,driver_version=COALESCE(?,driver_version) WHERE id=?`, now, body.Version, id); err != nil {
			return err
		}
		rows, err := query(ctx, tx, "SELECT host FROM egress_blacklist WHERE enabled=1 ORDER BY host,id")
		if err != nil {
			return err
		}
		for _, item := range rows {
			hosts = append(hosts, item.text("host"))
		}
		return nil
	}, func() {
		if t != nil {
			app.sampleEgress(id, *t, now)
		}
	})
	return row{"ok": true, "accepted": accepted, "blacklist": hosts}, err
}

func (app *panel) sampleEgress(id string, t egressTraffic, now int64) {
	app.egressMu.Lock()
	defer app.egressMu.Unlock()
	previous := app.live[id]
	sample := liveSample{egressTraffic: t, received: now}
	if previous.Run == t.Run {
		elapsed := t.Sampled - previous.Sampled
		if elapsed <= 0 {
			return
		}
		if elapsed <= 15 && now-previous.received < 15 && t.Upload >= previous.Upload && t.Download >= previous.Download {
			sample.rates = &[2]float64{float64(t.Upload-previous.Upload) / elapsed, float64(t.Download-previous.Download) / elapsed}
		}
	}
	app.live[id] = sample
}

func blacklistRows(ctx context.Context, db querier) ([]row, error) {
	rows, err := query(ctx, db, "SELECT id,host,enabled,created,updated FROM egress_blacklist ORDER BY host,id")
	for _, item := range rows {
		item["enabled"] = item.number("enabled") == 1
	}
	return rows, err
}

func (app *panel) blacklistChange(_ http.ResponseWriter, r *http.Request) (any, error) {
	create := r.Method == "POST"
	var id int64
	var err error
	if !create {
		id, err = idParam(r)
		if err != nil {
			return nil, err
		}
	}
	var body struct {
		Host    *string
		Enabled *bool
	}
	if r.Method != "DELETE" {
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		if create && body.Host == nil || body.Host == nil && body.Enabled == nil {
			return nil, failure(422, "请填写主机名或启用状态")
		}
		if body.Host != nil {
			host := strings.ToLower(strings.TrimSpace(*body.Host))
			_, ipErr := netip.ParseAddr(host)
			valid := len(host) <= 253 && ipErr != nil
			for _, label := range strings.Split(host, ".") {
				valid = valid && hostLabel.MatchString(label)
			}
			if !valid {
				return nil, failure(422, "黑名单只接受精确主机名")
			}
			body.Host = &host
		}
	}
	var result row
	err = app.write(r.Context(), func(tx *sql.Tx) error {
		var updated sql.Result
		var err error
		now := unixNow()
		switch r.Method {
		case "POST":
			updated, err = tx.Exec("INSERT INTO egress_blacklist(host,enabled,created,updated) VALUES(?,1,?,?)", body.Host, now, now)
		case "PATCH":
			updated, err = tx.Exec("UPDATE egress_blacklist SET host=COALESCE(?,host),enabled=COALESCE(?,enabled),updated=? WHERE id=?", body.Host, body.Enabled, now, id)
		case "DELETE":
			updated, err = tx.Exec("DELETE FROM egress_blacklist WHERE id=?", id)
		}
		if err != nil {
			var dbErr *sqlite.Error
			if errors.As(err, &dbErr) && dbErr.Code() == 2067 {
				return failure(409, "该主机已在出口黑名单中")
			}
			return err
		}
		if count, _ := updated.RowsAffected(); count == 0 {
			return failure(404, "出口黑名单规则不存在")
		}
		if create {
			id, err = updated.LastInsertId()
			if err != nil {
				return err
			}
		}
		if r.Method == "DELETE" {
			result = row{"ok": true}
			return nil
		}
		result, err = one(r.Context(), tx, "SELECT id,host,enabled,created,updated FROM egress_blacklist WHERE id=?", id)
		if err == nil {
			result["enabled"] = result.number("enabled") == 1
		}
		return err
	})
	return result, err
}

func (app *panel) testEgress(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, ctx := r.PathValue("id"), r.Context()
	item, err := deviceRow(ctx, app.database, id)
	if err != nil {
		return nil, err
	}
	if !online(item, unixNow()) {
		return nil, failure(409, "电脑尚未连接，请先安装并启动连接器")
	}
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", item.number("port")), User: url.UserPassword(id, item.text("proxy_password"))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	client := http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("https://myip.ipip.net")
	var ip string
	if err == nil {
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err == nil && response.StatusCode == 200 {
			if address, err := netip.ParseAddr(ipv4Pattern.FindString(string(data))); err == nil {
				ip = address.String()
			}
		}
	}
	now := unixNow()
	err = app.write(ctx, func(tx *sql.Tx) error {
		item, err := deviceRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if ip == "" {
			return deviceEvent(tx, item, "ip_test", "failed", "connection_failed")
		}
		if _, err := tx.Exec(`UPDATE egress_devices SET last_ip=?,last_test=? WHERE id=?`, ip, now, id); err != nil {
			return err
		}
		item["last_ip"] = ip
		return deviceEvent(tx, item, "ip_test", "success", "")
	})
	if err != nil {
		return nil, err
	}
	if ip == "" {
		return nil, failure(503, "出口检测失败，请检查电脑网络与连接器状态")
	}
	return row{"ip": ip, "tested_at": now}, nil
}
