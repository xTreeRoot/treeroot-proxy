package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
	hg "treeroot/gopanel/homegateway"
)

const homeTrafficSchema = `
CREATE TABLE IF NOT EXISTS home_traffic_totals(gateway_id TEXT NOT NULL,route TEXT NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,started INTEGER NOT NULL,last_seen INTEGER NOT NULL,speed_upload REAL,speed_download REAL,PRIMARY KEY(gateway_id,route));
CREATE TABLE IF NOT EXISTS home_traffic_checkpoints(gateway_id TEXT NOT NULL,generation TEXT NOT NULL,route TEXT NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,boot TEXT NOT NULL,monotonic INTEGER NOT NULL,sampled INTEGER NOT NULL,PRIMARY KEY(gateway_id,generation,route));
CREATE TABLE IF NOT EXISTS home_traffic_minutes(gateway_id TEXT NOT NULL,route TEXT NOT NULL,bucket INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(gateway_id,route,bucket));
`

func validateHomeTraffic(s *hg.TrafficSample) error {
	if s == nil {
		return nil
	} // Compatible with already-installed agents.
	if len(s.Generation) < 16 || len(s.Generation) > 64 || s.At < 0 || s.At > time.Now().Add(5*time.Minute).UnixMilli() || s.Monotonic < 0 || s.Started < 0 || s.Started > unixNow()+300 || len(s.Routes) != 2 {
		return failure(422, "家庭流量采样无效")
	}
	seen := map[string]bool{}
	for _, v := range s.Routes {
		if (v.Route != "direct" && v.Route != "proxy") || seen[v.Route] || v.Upload < 0 || v.Download < 0 || v.Upload > 1<<60 || v.Download > 1<<60 {
			return failure(422, "家庭流量计数无效")
		}
		seen[v.Route] = true
	}
	return nil
}
func storeHomeTraffic(ctx context.Context, tx *sql.Tx, id string, s hg.Sync) error {
	if s.Traffic == nil {
		return nil
	}
	t := s.Traffic
	now := unixNow()
	for _, v := range t.Routes {
		prior, e := one(ctx, tx, "SELECT upload,download,boot,monotonic,sampled FROM home_traffic_checkpoints WHERE gateway_id=? AND generation=? AND route=?", id, t.Generation, v.Route)
		fresh := errors.Is(e, sql.ErrNoRows)
		if e != nil && !fresh {
			return e
		}
		// A retried frame or older sample cannot bill traffic twice. A new local
		// ledger generation establishes an independent checkpoint, preserving totals.
		if !fresh && t.At <= prior.number("sampled") {
			continue
		}
		up, down := v.Upload, v.Download
		if !fresh {
			up = max(0, v.Upload-prior.number("upload"))
			down = max(0, v.Download-prior.number("download"))
		}
		var rateUp, rateDown any
		elapsed := t.Monotonic - prior.number("monotonic")
		if !fresh && s.Boot == prior.text("boot") && elapsed >= 250 && elapsed <= 15000 && t.At >= time.Now().Add(-15*time.Second).UnixMilli() && v.Upload >= prior.number("upload") && v.Download >= prior.number("download") {
			rateUp = float64(up) * 1000 / float64(elapsed)
			rateDown = float64(down) * 1000 / float64(elapsed)
		}
		if _, e = tx.Exec(`INSERT INTO home_traffic_totals VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(gateway_id,route) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download,started=MIN(started,excluded.started),last_seen=excluded.last_seen,speed_upload=excluded.speed_upload,speed_download=excluded.speed_download`, id, v.Route, up, down, t.Started, now, rateUp, rateDown); e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT INTO home_traffic_checkpoints VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(gateway_id,generation,route) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download),boot=excluded.boot,monotonic=excluded.monotonic,sampled=excluded.sampled`, id, t.Generation, v.Route, v.Upload, v.Download, s.Boot, t.Monotonic, t.At); e != nil {
			return e
		}
		// Offline catch-up is explicitly shown by report time; do not invent the
		// distribution of bytes across hours when the control channel was offline.
		if _, e = tx.Exec(`INSERT INTO home_traffic_minutes VALUES(?,?,?,?,?) ON CONFLICT(gateway_id,route,bucket) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download`, id, v.Route, now/60*60, up, down); e != nil {
			return e
		}
	}
	_, e := tx.Exec("DELETE FROM home_traffic_minutes WHERE gateway_id=? AND bucket<?", id, now-31*86400)
	return e
}
func (app *panel) homeOverview(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	id := r.PathValue("id")
	g, e := homeRow(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	hours := 24
	if r.URL.Query().Get("hours") == "168" {
		hours = 168
	} else if h := r.URL.Query().Get("hours"); h != "" && h != "24" {
		return nil, failure(422, "时间范围无效")
	}
	now := unixNow()
	since := now - int64(hours)*3600
	day := now - 86400
	totals, e := query(ctx, tx, `SELECT t.route,t.upload,t.download,t.started,t.last_seen,t.speed_upload,t.speed_download,COALESCE((SELECT SUM(m.upload) FROM home_traffic_minutes m WHERE m.gateway_id=t.gateway_id AND m.route=t.route AND m.bucket>=?),0) day_upload,COALESCE((SELECT SUM(m.download) FROM home_traffic_minutes m WHERE m.gateway_id=t.gateway_id AND m.route=t.route AND m.bucket>=?),0) day_download FROM home_traffic_totals t WHERE t.gateway_id=? ORDER BY t.route`, day/60*60, day/60*60, id)
	if e != nil {
		return nil, e
	}
	online := g.number("enabled") == 1 && g.number("last_seen") > now-30
	started := int64(0)
	for _, v := range totals {
		if !online || v.number("last_seen") < now-15 {
			v["speed_upload"] = nil
			v["speed_download"] = nil
		}
		if started == 0 || v.number("started") < started {
			started = v.number("started")
		}
	}
	buckets, e := query(ctx, tx, "SELECT bucket/3600*3600 ts,route,SUM(upload+download) bytes FROM home_traffic_minutes WHERE gateway_id=? AND bucket>=? GROUP BY ts,route ORDER BY ts", id, since/3600*3600)
	if e != nil {
		return nil, e
	}
	indexed := map[int64]row{}
	for _, b := range buckets {
		ts := b.number("ts")
		if indexed[ts] == nil {
			indexed[ts] = row{"ts": ts, "direct": int64(0), "proxy": int64(0)}
		}
		indexed[ts][b.text("route")] = b.number("bytes")
	}
	series := []row{}
	for ts := since / 3600 * 3600; ts <= now; ts += 3600 {
		v := indexed[ts]
		if v == nil {
			v = row{"ts": ts, "direct": int64(0), "proxy": int64(0)}
		}
		series = append(series, v)
	}
	destinations, e := query(ctx, tx, `SELECT target,action,COUNT(*) connections FROM home_events WHERE gateway_id=? AND ts>=? GROUP BY target,action ORDER BY connections DESC,target LIMIT 40`, id, since)
	if e != nil {
		return nil, e
	}
	return row{"since": since, "until": now, "started": started, "online": online, "totals": totals, "series": series, "destinations": destinations, "measurement": "wan_ip_bytes", "time_basis": "reported_at"}, nil
}
