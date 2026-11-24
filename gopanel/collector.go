package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type proxyStats struct {
	at        time.Time
	run       string
	rates     map[int64][2]float64
	lastError string
}

func (app *panel) rates(id int64) row {
	app.collectorMu.Lock()
	defer app.collectorMu.Unlock()
	stats := app.proxyStats
	status := row{"last_poll": nil, "last_error": nil}
	if !stats.at.IsZero() {
		status["last_poll"] = stats.at.Unix()
	}
	if stats.lastError != "" {
		status["last_error"] = stats.lastError
	}
	result := row{"collector": status, "speed_upload": nil, "speed_download": nil}
	if len(stats.rates) > 0 && time.Since(stats.at) < 30*time.Second && stats.lastError == "" {
		var up, down float64
		for user, rate := range stats.rates {
			if id == 0 || user == id {
				up += rate[0]
				down += rate[1]
			}
		}
		result["speed_upload"], result["speed_download"] = up, down
	}
	return result
}

func (app *panel) collect(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		err := app.collectOnce(ctx)
		app.collectorMu.Lock()
		app.proxyStats.lastError = ""
		if err != nil && ctx.Err() == nil {
			app.proxyStats.lastError = "采集失败"
		}
		app.collectorMu.Unlock()
		if ctx.Err() == nil {
			logError("Xray collection", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (app *panel) collectOnce(ctx context.Context) error {
	statsErr := app.collectStats(ctx)
	logErr := app.collectAccess(ctx)
	maintErr := app.write(ctx, func(tx *sql.Tx) error {
		now := unixNow()
		if err := syncTotals(tx, ""); err != nil {
			return err
		}
		if err := disconnectEvents(tx, now, ""); err != nil {
			return err
		}
		for _, statement := range []struct {
			sql    string
			cutoff int64
		}{
			{"DELETE FROM traffic WHERE ts<?", now - 31*86400},
			{"DELETE FROM events WHERE ts<?", now - 7*86400},
			{"DELETE FROM egress_events WHERE ts<?", now - 7*86400},
			{"DELETE FROM sessions WHERE expires<?", now},
			{"DELETE FROM login_attempts WHERE ts<?", now - 900},
		} {
			if _, err := tx.Exec(statement.sql, statement.cutoff); err != nil {
				return err
			}
		}
		return nil
	})
	return errors.Join(statsErr, logErr, maintErr)
}

func state(tx *sql.Tx, key string, value any) error {
	_, err := tx.Exec("INSERT INTO state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func (app *panel) collectStats(ctx context.Context) error {
	runID := func() (string, error) {
		output, err := command(5*time.Second, "systemctl", "show", "-p", "InvocationID", "--value", "xray")
		return strings.TrimSpace(string(output)), err
	}
	run, err := runID()
	if err != nil {
		return err
	}
	if run == "" {
		return errors.New("Xray invocation unavailable")
	}
	raw, err := app.xray("statsquery", "-pattern=user>>>")
	if err != nil {
		return err
	}
	after, err := runID()
	if err != nil {
		return err
	}
	if after != run {
		return errors.New("Xray restarted during collection")
	}
	var response struct {
		Stat []struct {
			Name  string
			Value int64
		}
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	now := time.Now()
	app.collectorMu.Lock()
	previous := app.proxyStats
	app.collectorMu.Unlock()
	elapsed := now.Sub(previous.at).Seconds()
	ready := previous.run == run && elapsed > 0
	rates := map[int64][2]float64{}
	err = app.write(ctx, func(tx *sql.Tx) error {
		users, err := query(ctx, tx, "SELECT id,email FROM users")
		if err != nil {
			return err
		}
		byEmail := map[string]int64{}
		for _, user := range users {
			id := user.number("id")
			byEmail[user.text("email")] = id
			if ready {
				rates[id] = [2]float64{}
			}
		}
		counters, err := query(ctx, tx, "SELECT name,run,value FROM counters")
		if err != nil {
			return err
		}
		saved := map[string]row{}
		for _, item := range counters {
			saved[item.text("name")] = item
		}
		deltas := map[int64][2]int64{}
		for _, sample := range response.Stat {
			fields := strings.Split(sample.Name, ">>>")
			if len(fields) != 4 || fields[0] != "user" || fields[2] != "traffic" || sample.Value < 0 {
				continue
			}
			id := byEmail[fields[1]]
			if id == 0 {
				continue
			}
			var direction int
			switch fields[3] {
			case "uplink":
				direction = 0
			case "downlink":
				direction = 1
			default:
				continue
			}
			prev := saved[sample.Name]
			delta := sample.Value
			if prev.text("run") == run && sample.Value >= prev.number("value") {
				delta -= prev.number("value")
				if ready {
					rate := rates[id]
					rate[direction] += float64(delta) / elapsed
					rates[id] = rate
				}
				if delta == 0 {
					continue
				}
			}
			if _, err := tx.Exec("INSERT INTO counters VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET run=excluded.run,value=excluded.value", sample.Name, run, sample.Value); err != nil {
				return err
			}
			if delta != 0 {
				traffic := deltas[id]
				traffic[direction] += delta
				deltas[id] = traffic
			}
		}
		for id, traffic := range deltas {
			if _, err := tx.Exec("UPDATE users SET upload=upload+?,download=download+? WHERE id=?", traffic[0], traffic[1], id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO traffic VALUES(?,?,?,?) ON CONFLICT(ts,user_id) DO UPDATE
				SET upload=upload+excluded.upload,download=download+excluded.download`, now.Unix()/60*60, id, traffic[0], traffic[1]); err != nil {
				return err
			}
		}
		return state(tx, "limits_stats_sample", float64(now.UnixMilli())/1000)
	})
	if err == nil {
		app.collectorMu.Lock()
		app.proxyStats = proxyStats{at: now, run: run, rates: rates}
		app.collectorMu.Unlock()
	}
	return err
}

var accessPattern = regexp.MustCompile(`accepted\s+(tcp|udp):(.+):([0-9]+)\s+\[.*?\].*?email:\s*(\S+)`)

type accessEvent struct {
	ts, id, port    int64
	domain, network string
}
type domainRecord struct{ count, last int64 }
type domainKey struct {
	id   int64
	host string
}

func (app *panel) collectAccess(ctx context.Context) error {
	files, err := app.openAccess(ctx)
	if err != nil || len(files) == 0 {
		return err
	}
	for _, file := range files {
		defer file.stream.Close()
	}
	users, err := query(ctx, app.database, "SELECT id,email FROM users")
	if err != nil {
		return err
	}
	byEmail := map[string]int64{}
	for _, user := range users {
		byEmail[user.text("email")] = user.number("id")
	}
	var cursor [2]int64
	events := make([]accessEvent, 0, 2000)
	domains := map[domainKey]domainRecord{}
	remaining := 2000
	for _, file := range files {
		cursor = [2]int64{file.inode, file.offset}
		if _, err := file.stream.Seek(file.offset, 0); err != nil {
			return err
		}
		reader := bufio.NewReader(file.stream)
		for remaining > 0 {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			cursor[1] += int64(len(line))
			remaining--
			match := accessPattern.FindStringSubmatch(line)
			if len(match) == 0 {
				continue
			}
			id := byEmail[match[4]]
			if id == 0 {
				continue
			}
			port, err := strconv.ParseInt(match[3], 10, 64)
			if err != nil {
				continue
			}
			host := []rune(strings.ToLower(strings.Trim(match[2], "[]")))
			domain := string(host[:min(len(host), 253)])
			ts := unixNow()
			if len(line) >= 19 {
				if parsed, err := time.Parse("2006/01/02 15:04:05", line[:19]); err == nil {
					ts = parsed.Unix()
				}
			}
			events = append(events, accessEvent{ts, id, port, domain, match[1]})
			key := domainKey{id, domain}
			seen := domains[key]
			seen.count++
			seen.last = max(seen.last, ts)
			domains[key] = seen
		}
		if remaining == 0 {
			break
		}
	}
	next, _ := json.Marshal(cursor)
	return app.write(ctx, func(tx *sql.Tx) error {
		if len(events) > 0 {
			insert, err := tx.Prepare("INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,?,?,?,?)")
			if err != nil {
				return err
			}
			defer insert.Close()
			for _, item := range events {
				if _, err := insert.Exec(item.ts, item.id, item.domain, item.port, item.network); err != nil {
					return err
				}
			}
			upsert, err := tx.Prepare(`INSERT INTO domains VALUES(?,?,?,?) ON CONFLICT(user_id,domain) DO UPDATE
				SET connections=connections+excluded.connections,last_seen=MAX(last_seen,excluded.last_seen)`)
			if err != nil {
				return err
			}
			defer upsert.Close()
			for key, item := range domains {
				if _, err := upsert.Exec(key.id, key.host, item.count, item.last); err != nil {
					return err
				}
			}
		}
		return state(tx, "log_cursor", string(next))
	})
}

type accessFile struct {
	stream        *os.File
	inode, offset int64
}

func (app *panel) openAccess(ctx context.Context) ([]accessFile, error) {
	var cursor [2]int64
	saved, err := one(ctx, app.database, "SELECT value FROM state WHERE key='log_cursor'")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(saved.text("value")), &cursor); err != nil {
			return nil, err
		}
	}
	open := func(path string) (accessFile, error) {
		stream, err := os.Open(path)
		if err != nil {
			return accessFile{}, err
		}
		info, err := stream.Stat()
		if err != nil {
			stream.Close()
			return accessFile{}, err
		}
		inode := int64(info.Sys().(*syscall.Stat_t).Ino)
		var offset int64
		if cursor[0] == inode && cursor[1] >= 0 && cursor[1] <= info.Size() {
			offset = cursor[1]
		}
		return accessFile{stream, inode, offset}, nil
	}
	current, err := open(app.config.AccessLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cursor[0] != 0 && cursor[0] != current.inode {
		old, err := open(filepath.Join(filepath.Dir(app.config.AccessLog), "access.log.1"))
		if err == nil {
			if old.inode == cursor[0] {
				return []accessFile{old, current}, nil
			}
			old.stream.Close()
		}
	}
	return []accessFile{current}, nil
}
