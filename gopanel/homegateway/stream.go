package homegateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mihomo's authenticated /logs endpoint supports newline-delimited HTTP streaming.
// Keep only connection metadata; never forward arbitrary kernel logs or configuration.
var logPattern = regexp.MustCompile(`^\[(TCP|UDP)\]\s+([^ ]+)\s+-->\s+([^ ]+)\s+(.+)$`)
var denyLogPattern = regexp.MustCompile(`^\[(TCP|UDP)\] dial (REJECT|DIRECT|HG-[a-zA-Z0-9_. -]+) (?:\(match ([^\n]+)\) )?([^ ]+) --> ([^ ]+) error: `)

func ParseLog(payload string) (Event, bool) {
	var e Event
	m := logPattern.FindStringSubmatch(payload)
	failedConnection := false
	if m == nil {
		failed := denyLogPattern.FindStringSubmatch(payload)
		if failed == nil {
			return e, false
		}
		failedConnection = true
		m = []string{failed[0], failed[1], failed[4], failed[5], "match " + failed[3] + " using " + failed[2]}
	}
	source, _, err := net.SplitHostPort(m[2])
	if err != nil {
		return e, false
	}
	target, port, err := net.SplitHostPort(m[3])
	if err != nil {
		return e, false
	}
	e = Event{ID: Nonce(), TS: time.Now().Unix(), Source: source, Target: target, Protocol: strings.ToLower(m[1]), Rule: m[4]}
	e.Port, _ = strconv.Atoi(port)
	if failedConnection {
		e.Outcome = "failed"
	}
	switch {
	case strings.Contains(m[4], "REJECT"):
		e.Action = "reject"
		e.Outcome = "blocked"
	case strings.Contains(m[4], "DIRECT"):
		e.Action = "direct"
	case strings.Contains(m[4], "HG-"):
		e.Action = "proxy"
	default:
		return Event{}, false
	}
	if len(e.Target) > 253 || len(e.Rule) > 256 {
		return Event{}, false
	}
	return e, true
}
func (a *Agent) stream(ctx context.Context, out chan<- Event) {
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}
	defer client.CloseIdleConnections()
	for ctx.Err() == nil {
		request, _ := http.NewRequestWithContext(ctx, "GET", a.api+"/logs?level=info", nil)
		request.Header.Set("Authorization", "Bearer "+a.secret)
		res, err := client.Do(request)
		if err == nil {
			if res.StatusCode == 200 {
				scanner := bufio.NewScanner(res.Body)
				scanner.Buffer(make([]byte, 4096), 16384)
				for scanner.Scan() {
					var line struct {
						Payload string `json:"payload"`
					}
					if json.Unmarshal(scanner.Bytes(), &line) != nil {
						continue
					}
					if e, ok := ParseLog(line.Payload); ok {
						select {
						case out <- e:
						case <-ctx.Done():
							res.Body.Close()
							return
						default:
							a.droppedEvents.Add(1)
						}
					}
				}
			}
			res.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
