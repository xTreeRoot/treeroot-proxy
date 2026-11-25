package homegateway

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

// Socket marks distinguish the actual WAN paths used by household adapters.
// Management HTTPS and health probes are unmarked; shared DNS follows the
// configured resolver path and can contribute to the marked WAN counters.
const directMark = 0x78
const proxyMark = 0x79
const directAdapter = "HG-DIRECT"

type RouteTraffic struct {
	Route    string `json:"route"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}
type TrafficSample struct {
	Generation string         `json:"generation"`
	At         int64          `json:"at"`
	Monotonic  int64          `json:"monotonic"`
	Started    int64          `json:"started"`
	Routes     []RouteTraffic `json:"routes"`
}

// Keep metering independent of policy replacement. Count IP bytes on the actual
// WAN, including overhead/retransmits, rather than estimating from log counts.
const trafficRules = `add table inet treeroot_traffic
add counter inet treeroot_traffic direct_up
add counter inet treeroot_traffic direct_down
add counter inet treeroot_traffic proxy_up
add counter inet treeroot_traffic proxy_down
add chain inet treeroot_traffic stamp { type route hook output priority -140; policy accept; }
add rule inet treeroot_traffic stamp meta mark { 0x78, 0x79 } ct mark set meta mark
add chain inet treeroot_traffic upload { type filter hook postrouting priority 150; policy accept; }
add rule inet treeroot_traffic upload oifname "eth0" meta mark 0x78 counter name direct_up
add rule inet treeroot_traffic upload oifname "eth0" meta mark 0x79 counter name proxy_up
add chain inet treeroot_traffic download { type filter hook prerouting priority -140; policy accept; }
add rule inet treeroot_traffic download iifname "eth0" ct direction reply ct mark 0x78 counter name direct_down
add rule inet treeroot_traffic download iifname "eth0" ct direction reply ct mark 0x79 counter name proxy_down
`

func (a *Agent) ensureTraffic(ctx context.Context) error {
	if _, e := run(ctx, "nft", "list", "table", "inet", "treeroot_traffic"); e == nil {
		return nil
	}
	check := exec.CommandContext(ctx, "nft", "-c", "-f", "-")
	check.Stdin = strings.NewReader(trafficRules)
	if check.Run() != nil {
		return errors.New("家庭出口流量计数器校验失败")
	}
	for _, route := range []string{"direct", "proxy"} {
		m := a.ledger.Meters["wan:"+route]
		m.Last = [2]int64{}
		a.ledger.Meters["wan:"+route] = m
	}
	if e := a.persistLedger(); e != nil {
		return e
	}
	apply := exec.CommandContext(ctx, "nft", "-f", "-")
	apply.Stdin = strings.NewReader(trafficRules)
	if apply.Run() != nil {
		return errors.New("家庭出口流量计数器安装失败")
	}
	return nil
}
func (a *Agent) trafficCounters(ctx context.Context) (map[string][2]int64, error) {
	if e := a.ensureTraffic(ctx); e != nil {
		return nil, e
	}
	raw, e := run(ctx, "nft", "-j", "list", "table", "inet", "treeroot_traffic")
	if e != nil {
		return nil, errors.New("家庭出口流量计数器读取失败")
	}
	var obj struct {
		NFT []struct {
			Counter *struct {
				Name  string `json:"name"`
				Bytes int64  `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if e = json.Unmarshal(raw, &obj); e != nil {
		return nil, e
	}
	values := map[string][2]int64{}
	found := 0
	for _, entry := range obj.NFT {
		if entry.Counter == nil {
			continue
		}
		c := entry.Counter
		for _, route := range []string{"direct", "proxy"} {
			for direction, suffix := range []string{"up", "down"} {
				if c.Name == route+"_"+suffix {
					v := values["wan:"+route]
					v[direction] = c.Bytes
					values["wan:"+route] = v
					found++
				}
			}
		}
	}
	if found != 4 {
		return nil, errors.New("家庭出口流量计数器不完整")
	}
	return values, nil
}
func (a *Agent) trafficSample() *TrafficSample {
	s := &TrafficSample{Generation: a.ledger.TrafficGeneration, Started: a.ledger.TrafficStarted, At: a.sampledAt, Monotonic: a.sampledMono, Routes: []RouteTraffic{}}
	for _, route := range []string{"direct", "proxy"} {
		m := a.ledger.Meters["wan:"+route]
		s.Routes = append(s.Routes, RouteTraffic{Route: route, Upload: m.Upload, Download: m.Download})
	}
	return s
}
