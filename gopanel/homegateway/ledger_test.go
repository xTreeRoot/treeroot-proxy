package homegateway

import (
	"testing"
	"time"
)

func TestLedgerCounterDeltasActivityAndDayChange(t *testing.T) {
	l := Ledger{Meters: map[string]Meter{}}
	now := time.Date(2026, 11, 24, 15, 59, 59, 0, time.UTC)
	l.add(map[string][2]int64{"a": {100, 200}}, now)
	l.add(map[string][2]int64{"a": {150, 250}}, now)
	m := l.Meters["a"]
	if m.Upload != 150 || m.Download != 250 || m.Daily != 400 || m.LastActive != now.Unix() {
		t.Fatal(m)
	}
	l.add(map[string][2]int64{"a": {150, 250}}, now.Add(time.Second))
	m = l.Meters["a"]
	if m.Daily != 0 || m.Upload+m.Download != 400 || m.LastActive != now.Unix() {
		t.Fatal("idle counter or midnight reset", m)
	}
	l.add(map[string][2]int64{"a": {10, 20}}, now.Add(2*time.Second))
	m = l.Meters["a"]
	if m.Upload+m.Download != 430 || m.Daily != 30 {
		t.Fatal("counter generation reset", m)
	}
}
