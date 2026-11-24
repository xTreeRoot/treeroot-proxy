package main

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (app *panel) overview(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	params := r.URL.Query()
	id, idOK := queryInt(params, "user_id", 0)
	year, yOK := queryInt(params, "year", 0)
	month, mOK := queryInt(params, "month", 0)
	day, dOK := queryInt(params, "day", 0)
	if !idOK || !yOK || !mOK || !dOK || params.Has("year") && (year < 2000 || year > 9998) || params.Has("month") && (month < 1 || month > 12 || year == 0) || params.Has("day") && (day < 1 || day > 31 || month == 0) {
		return nil, failure(422, "请选择有效的年月日")
	}
	zone := queryValue(params, "time_zone")
	if !params.Has("time_zone") {
		zone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" || len(zone) > 64 {
		return nil, failure(422, "请选择有效的统计时区")
	}
	where, condition, domainWhere, args := "", "", "", []any{}
	if id != 0 {
		where, condition, domainWhere, args = " WHERE id=?", " AND user_id=?", " WHERE user_id=?", []any{id}
	}
	now := unixNow()
	result, err := one(ctx, tx, `SELECT COALESCE(SUM(upload),0) upload,COALESCE(SUM(download),0) download,COALESCE(SUM(CASE WHEN NOT EXISTS(SELECT 1 FROM user_archives a WHERE a.user_id=users.id) THEN 1 ELSE 0 END),0) users,COALESCE(SUM(enabled),0) active FROM users`+where, args...)
	if err != nil {
		return nil, err
	}
	dailyArgs := append([]any{now - 86400}, args...)
	daily, err := one(ctx, tx, `SELECT COALESCE(SUM(upload),0) day_upload,COALESCE(SUM(download),0) day_download FROM traffic WHERE ts>=?`+condition, dailyArgs...)
	if err != nil {
		return nil, err
	}
	connections, err := one(ctx, tx, `SELECT COUNT(*) connections FROM events WHERE ts>=?`+condition, dailyArgs...)
	if err != nil {
		return nil, err
	}
	domains, err := query(ctx, tx, `SELECT domain,SUM(connections) connections,MAX(last_seen) last_seen FROM domains`+domainWhere+` GROUP BY domain ORDER BY connections DESC LIMIT 12`, args...)
	if err != nil {
		return nil, err
	}
	granularity := "hour"
	since, until := now-86400, now+1
	start, end := time.Unix(now/3600*3600-23*3600, 0), time.Unix(now+1, 0)
	step := func(t time.Time) time.Time { return t.Add(time.Hour) }
	if year != 0 {
		start = time.Date(int(year), time.Month(max(month, 1)), int(max(day, 1)), 0, 0, 0, 0, location)
		if day != 0 && int64(start.Day()) != day {
			return nil, failure(422, "请选择有效的年月日")
		}
		switch {
		case day != 0:
			end = start.AddDate(0, 0, 1)
		case month != 0:
			granularity = "day"
			end = start.AddDate(0, 1, 0)
			step = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
		default:
			granularity = "month"
			end = start.AddDate(1, 0, 0)
			step = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
		}
		since, until = start.Unix(), end.Unix()
	}
	expression := "ts"
	if year == 0 {
		expression = "ts/3600*3600"
	}
	traffic, err := query(ctx, tx, `SELECT `+expression+` stamp,SUM(upload) upload,SUM(download) download FROM traffic WHERE ts>=? AND ts<?`+condition+` GROUP BY stamp ORDER BY stamp`, append([]any{since, until}, args...)...)
	if err != nil {
		return nil, err
	}
	series := []row{}
	for cursor, startIdx := start, 0; cursor.Before(end); cursor = step(cursor) {
		next := step(cursor).Unix()
		var up, down int64
		for startIdx < len(traffic) && traffic[startIdx].number("stamp") < next {
			if item := traffic[startIdx]; item.number("stamp") >= cursor.Unix() {
				up += item.number("upload")
				down += item.number("download")
			}
			startIdx++
		}
		series = append(series, row{"ts": cursor.Unix(), "upload": up, "download": down})
	}
	optional := func(value int64) any {
		if value == 0 {
			return nil
		}
		return value
	}
	result["series"], result["domains"], result["connections"] = series, domains, connections["connections"]
	result["trend"] = row{"year": optional(year), "month": optional(month), "day": optional(day), "granularity": granularity, "since": since, "until": until, "time_zone": zone, "retained_since": now - 31*86400}
	merge(result, daily, app.rates(id))
	return result, nil
}

func merge(target row, sources ...row) {
	for _, source := range sources {
		for key, value := range source {
			target[key] = value
		}
	}
}

func (app *panel) liveRates(devices []row, now int64) row {
	app.egressMu.Lock()
	defer app.egressMu.Unlock()
	var up, down float64
	var active, fresh int
	for _, device := range devices {
		if !online(device, now) {
			continue
		}
		active++
		sample := app.live[device.text("id")]
		if sample.rates != nil && now-sample.received < 15 {
			fresh++
			up += sample.rates[0]
			down += sample.rates[1]
		}
	}
	result := row{"speed_upload": nil, "speed_download": nil, "live_devices": fresh, "online_devices": active, "sampled_at": now}
	if fresh > 0 || active == 0 {
		result["speed_upload"], result["speed_download"] = up, down
	}
	return result
}

func deviceFilter(id, clause string) (string, []any) {
	if id != "" {
		return clause, []any{id}
	}
	return "", nil
}

func (app *panel) egressRates(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	id := r.URL.Query().Get("device_id")
	where, args := deviceFilter(id, " WHERE id=?")
	rows, err := query(ctx, tx, "SELECT id,enabled,deleted,public_key,last_seen FROM egress_devices"+where, args...)
	if err != nil {
		return nil, err
	}
	if id != "" && len(rows) == 0 {
		return nil, failure(404, "出口设备不存在")
	}
	return app.liveRates(rows, unixNow()), nil
}

func (app *panel) egressOverview(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	id := r.URL.Query().Get("device_id")
	period := strings.TrimSpace(r.URL.Query().Get("hours"))
	hours := int64(24)
	switch period {
	case "", "24":
	case "168":
		hours = 168
	default:
		return nil, failure(422, "统计范围仅支持最近 24 小时或 7 天")
	}
	now := unixNow()
	since := now - hours*3600
	rows, err := query(ctx, tx, `SELECT id,name,deleted,enabled,public_key,last_seen,driver_version FROM egress_devices ORDER BY deleted,created,id`)
	if err != nil {
		return nil, err
	}
	options, selected := []row{}, []row{}
	for _, item := range rows {
		options = append(options, row{"id": item["id"], "name": item["name"], "deleted": item.number("deleted") == 1})
		if id == "" || id == item.text("id") {
			selected = append(selected, item)
		}
	}
	if id != "" && len(selected) == 0 {
		return nil, failure(404, "出口设备不存在")
	}
	filter, args := deviceFilter(id, " AND device_id=?")
	where := " WHERE kind='request' AND ts>=? AND ts<=?" + filter
	window := append([]any{since, now}, args...)
	grouped, err := query(ctx, tx, `SELECT device_id,SUM(upload) upload,SUM(download) download,COUNT(*) connections,SUM(outcome='success') succeeded,MAX(ts) last_record FROM egress_events`+where+` GROUP BY device_id`, window...)
	if err != nil {
		return nil, err
	}
	groupByID := map[string]row{}
	for _, item := range grouped {
		groupByID[item.text("device_id")] = item
	}
	day, err := one(ctx, tx, `SELECT COALESCE(SUM(upload),0) day_upload,COALESCE(SUM(download),0) day_download,COUNT(*) day_connections,COALESCE(SUM(outcome='success'),0) day_succeeded FROM egress_events`+where, append([]any{now - 86400, now}, args...)...)
	if err != nil {
		return nil, err
	}
	filter, _ = deviceFilter(id, " WHERE device_id=?")
	result, err := one(ctx, tx, `SELECT COALESCE(SUM(upload),0) total_upload,COALESCE(SUM(download),0) total_download,MIN(since) total_since FROM egress_totals`+filter, args...)
	if err != nil {
		return nil, err
	}
	hourly, err := query(ctx, tx, `SELECT MIN((ts-?)/3600,?) bucket,SUM(upload) upload,SUM(download) download,COUNT(*) connections FROM egress_events`+where+` GROUP BY bucket`, append([]any{since, hours - 1}, window...)...)
	if err != nil {
		return nil, err
	}
	domains, err := query(ctx, tx, `SELECT domain,COUNT(*) connections,SUM(upload) upload,SUM(download) download FROM egress_events`+where+` AND domain<>'' GROUP BY domain ORDER BY connections DESC,domain LIMIT 12`, window...)
	if err != nil {
		return nil, err
	}
	series := make([]row, hours)
	for idx := range series {
		series[idx] = row{"ts": since + int64(idx)*3600, "upload": 0, "download": 0, "connections": 0}
	}
	for _, item := range hourly {
		idx := item.number("bucket")
		merge(series[idx], item.fields("upload", "download", "connections"))
	}
	devices := []row{}
	totals := row{"upload": int64(0), "download": int64(0), "connections": int64(0), "succeeded": int64(0)}
	var active, enabled, available, lastRecord int64
	for _, item := range selected {
		device := item.fields("id", "name", "last_seen", "driver_version")
		isOnline, isEnabled, isDeleted := online(item, now), item.number("enabled") == 1, item.number("deleted") == 1
		device["online"], device["enabled"], device["deleted"] = isOnline, isEnabled, isDeleted
		if isOnline {
			active++
		}
		if !isDeleted {
			available++
			if isEnabled {
				enabled++
			}
		}
		stats := groupByID[item.text("id")]
		for _, key := range []string{"upload", "download", "connections", "succeeded"} {
			device[key] = stats.number(key)
			totals[key] = totals.number(key) + stats.number(key)
		}
		device["last_record"] = stats["last_record"]
		lastRecord = max(lastRecord, stats.number("last_record"))
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool {
		left, right := devices[i], devices[j]
		l, r := left.number("upload")+left.number("download"), right.number("upload")+right.number("download")
		if l != r {
			return l > r
		}
		if left.text("name") != right.text("name") {
			return left.text("name") < right.text("name")
		}
		return left.text("id") < right.text("id")
	})
	result["last_record"] = nil
	if lastRecord != 0 {
		result["last_record"] = lastRecord
	}
	merge(result, day, totals, app.liveRates(selected, now), row{"since": since, "until": now, "hours": hours, "online": active, "available": available, "enabled": enabled, "device_options": options, "devices": devices, "domains": domains, "series": series})
	return result, nil
}

func (app *panel) egressRecords(ctx context.Context, tx *sql.Tx, r *http.Request) (any, error) {
	params := r.URL.Query()
	page, pOK := queryInt(params, "page", 1)
	size, sOK := queryInt(params, "page_size", 25)
	before, bOK := queryInt(params, "before_id", 0)
	start, aOK := queryInt(params, "start", 0)
	end, eOK := queryInt(params, "end", 0)
	search := strings.TrimSpace(params.Get("search"))
	kind, outcome := params.Get("kind"), params.Get("outcome")
	if !pOK || !sOK || !bOK || !aOK || !eOK || page < 1 || size < 1 || size > 100 || params.Has("before_id") && before < 1 || start < 0 || end < 0 || params.Has("end") && start > end || len([]rune(search)) > 253 || kind != "" && kind != "request" && kind != "device" || outcome != "" && outcome != "success" && outcome != "failed" {
		return nil, failure(422, "查询参数无效")
	}
	where, args := " WHERE e.ts>=?", []any{max(start, unixNow()-7*86400)}
	for _, filter := range []struct{ column, value string }{{"device_id", params.Get("device_id")}, {"kind", kind}, {"outcome", outcome}} {
		if filter.value != "" {
			where += " AND e." + filter.column + "=?"
			args = append(args, filter.value)
		}
	}
	if params.Has("end") {
		where += " AND e.ts<=?"
		args = append(args, end)
	}
	if search != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
		where += ` AND (e.domain LIKE ? ESCAPE '\' OR e.exit_ip LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern)
	}
	var cursor any = before
	if before == 0 {
		item, err := one(ctx, tx, "SELECT MAX(e.id) id FROM egress_events e"+where, args...)
		if err != nil {
			return nil, err
		}
		cursor = item["id"]
		before = item.number("id")
	}
	where += " AND e.id<=?"
	args = append(args, before)
	count, err := one(ctx, tx, "SELECT COUNT(*) n FROM egress_events e"+where, args...)
	if err != nil {
		return nil, err
	}
	total := count.number("n")
	pages := max(1, (total+size-1)/size)
	page = min(page, pages)
	items, err := query(ctx, tx, `SELECT e.id,e.device_id,e.ts,e.kind,e.action,e.outcome,e.domain,e.port,
		e.method,e.upload,e.download,e.duration_ms,e.exit_ip,e.detail,d.name
		FROM (SELECT e.id FROM egress_events e`+where+` ORDER BY e.id DESC LIMIT ? OFFSET ?) page
		JOIN egress_events e ON e.id=page.id JOIN egress_devices d ON d.id=e.device_id ORDER BY e.id DESC`,
		append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, err
	}
	return row{"items": items, "total": total, "page": page, "page_size": size, "pages": pages, "before_id": cursor}, nil
}
