package homegateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Meter struct {
	Upload     int64    `json:"upload"`
	Download   int64    `json:"download"`
	Day        string   `json:"day"`
	Daily      int64    `json:"daily"`
	Last       [2]int64 `json:"last"`
	LastActive int64    `json:"last_active"`
}
type Ledger struct {
	Boot              string           `json:"boot"`
	Meters            map[string]Meter `json:"meters"`
	TrafficGeneration string           `json:"traffic_generation,omitempty"`
	TrafficStarted    int64            `json:"traffic_started,omitempty"`
}

func dayAt(t time.Time) string {
	return t.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format(time.DateOnly)
}
func (a *Agent) readLedger() error {
	raw, e := os.ReadFile(filepath.Join(a.Config.StateDir, "usage.json"))
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if len(raw) > 0 {
		if e = json.Unmarshal(raw, &a.ledger); e != nil {
			return e
		}
	}
	if a.ledger.Meters == nil {
		a.ledger.Meters = map[string]Meter{}
	}
	if a.ledger.TrafficGeneration == "" {
		a.ledger.TrafficGeneration = Nonce()
		a.ledger.TrafficStarted = time.Now().Unix()
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if a.ledger.Boot != string(boot) {
		for k, m := range a.ledger.Meters {
			m.Last = [2]int64{}
			a.ledger.Meters[k] = m
		}
		a.ledger.Boot = string(boot)
	}
	return nil
}
func (a *Agent) sample(ctx context.Context) error {
	values := a.counters(ctx)
	traffic, e := a.trafficCounters(ctx)
	if e != nil {
		return e
	}
	for key, v := range traffic {
		values[key] = v
	}
	a.ledger.add(values, time.Now())
	a.sampledAt = time.Now().UnixMilli()
	a.sampledMono = time.Since(a.started).Milliseconds()
	return a.persistLedger()
}
func (a *Agent) persistLedger() error {
	data, e := json.Marshal(a.ledger)
	if e != nil {
		return e
	}
	return Atomic(filepath.Join(a.Config.StateDir, "usage.json"), data, 0600)
}
func (l *Ledger) add(values map[string][2]int64, now time.Time) {
	day := dayAt(now)
	for mac, v := range values {
		m := l.Meters[mac]
		if m.Day != day {
			m.Day = day
			m.Daily = 0
		}
		for i, raw := range v {
			delta := raw - m.Last[i]
			if delta < 0 {
				delta = raw
			}
			if delta > 0 {
				m.LastActive = now.Unix()
			}
			if i == 0 {
				m.Upload += delta
			} else {
				m.Download += delta
			}
			m.Daily += delta
		}
		m.Last = v
		l.Meters[mac] = m
	}
}
func (a *Agent) resetCounters() error {
	for k, m := range a.ledger.Meters {
		if len(k) > 4 && k[:4] == "wan:" {
			continue
		}
		m.Last = [2]int64{}
		a.ledger.Meters[k] = m
	}
	data, e := json.Marshal(a.ledger)
	if e != nil {
		return e
	}
	return Atomic(filepath.Join(a.Config.StateDir, "usage.json"), data, 0600)
}
