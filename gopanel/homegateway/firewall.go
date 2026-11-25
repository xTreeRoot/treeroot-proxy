package homegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Firewall is an isolated nftables table. It never replaces OpenWrt's fw4 table.
func Firewall(c Config, devices []Device, usage map[string][2]int64) (string, error) {
	if e := c.Validate(); e != nil {
		return "", e
	}
	var b strings.Builder
	b.WriteString("add table inet treeroot_home\ndelete table inet treeroot_home\ntable inet treeroot_home {\n")
	byMAC := map[string]Device{}
	for _, d := range devices {
		if m, e := MAC(d.MAC); e != nil || m != d.MAC {
			return "", errors.New("设备身份未规范化")
		}
		if d.IP != "" {
			ip, e := netip.ParseAddr(d.IP)
			if e != nil || !ip.Is4() || !netip.MustParsePrefix("192.168.77.0/24").Contains(ip) || ip.String() != d.IP {
				return "", errors.New("设备地址不属于 LAN")
			}
		}
		if _, e := MAC(d.MAC); e != nil {
			return "", e
		}
		byMAC[d.MAC] = d
	}
	policies := map[string]DevicePolicy{}
	for _, p := range c.Devices {
		policies[p.MAC] = p
	}
	keys := make([]string, 0, len(byMAC))
	for m := range byMAC {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	var pre, post strings.Builder
	for _, mac := range keys {
		d := byMAC[mac]
		if d.IP == "" {
			continue
		}
		if _, e := MAC(mac); e != nil {
			return "", e
		}
		p := policies[mac]
		key := strings.ReplaceAll(mac, ":", "")
		fmt.Fprintf(&b, " counter up_%s { }\n counter down_%s { }\n", key, key)
		left := int64(-1)
		u := usage[mac]
		if p.TotalBytes > 0 {
			left = max(0, p.TotalBytes-u[0])
		}
		if p.DailyBytes > 0 {
			daily := max(0, p.DailyBytes-u[1])
			if left < 0 || daily < left {
				left = daily
			}
		}
		if left > 0 {
			fmt.Fprintf(&b, " quota q_%s { over %d bytes; }\n", key, left)
		}
		if p.SpeedBPS > 0 {
			rate := max(1, p.SpeedBPS/8)
			fmt.Fprintf(&b, " limit l_%s { rate over %d bytes/second; burst %d bytes; }\n", key, rate, max(1500, rate/10))
		}
		up := fmt.Sprintf("iifname \"br-lan\" ether saddr %s ip saddr %s", mac, d.IP)
		down := fmt.Sprintf("oifname \"br-lan\" ip daddr %s", d.IP)
		if p.Blocked || left == 0 {
			fmt.Fprintf(&pre, " %s counter drop\n", up)
			fmt.Fprintf(&post, " %s counter drop\n", down)
			continue
		}
		if left > 0 {
			fmt.Fprintf(&pre, " %s quota name q_%s counter drop\n", up, key)
			fmt.Fprintf(&post, " %s quota name q_%s counter drop\n", down, key)
		}
		if p.SpeedBPS > 0 {
			fmt.Fprintf(&pre, " %s limit name l_%s counter drop\n", up, key)
			fmt.Fprintf(&post, " %s limit name l_%s counter drop\n", down, key)
		}
		fmt.Fprintf(&pre, " %s counter name up_%s return\n", up, key)
		fmt.Fprintf(&post, " %s counter name down_%s return\n", down, key)
	}
	b.WriteString(" chain admission { type filter hook prerouting priority -155; policy accept;\n iifname != \"br-lan\" return\n meta nfproto ipv6 counter drop\n udp sport 68 udp dport 67 return\n udp dport 53 return\n tcp dport 53 return\n")
	b.WriteString(pre.String())
	b.WriteString(" iifname \"br-lan\" counter drop\n }\n chain accounting_download { type filter hook postrouting priority -155; policy accept;\n")
	b.WriteString(post.String())
	b.WriteString(" }\n chain dns { type nat hook prerouting priority -110; policy accept;\n iifname \"br-lan\" udp dport 53 redirect to :53\n iifname \"br-lan\" tcp dport 53 redirect to :53\n }\n chain transparent { type filter hook prerouting priority -145; policy accept;\n iifname != \"br-lan\" return\n fib daddr type local return\n ip daddr { 192.168.77.0/24, 224.0.0.0/4, 255.255.255.255 } return\n udp dport { 53, 67, 68 } return\n tcp dport 53 return\n meta l4proto { tcp, udp } meta mark set 0x77 tproxy to :7895 accept\n }\n}\n")
	return b.String(), nil
}
func (a *Agent) applyFirewall(ctx context.Context, c Config, devices []Device) error {
	usage := map[string][2]int64{}
	for mac, m := range a.ledger.Meters {
		daily := m.Daily
		if m.Day != dayAt(time.Now()) {
			daily = 0
		}
		usage[mac] = [2]int64{m.Upload + m.Download, daily}
	}
	table, e := Firewall(c, devices, usage)
	if e != nil {
		return e
	}
	check := exec.CommandContext(ctx, "nft", "-c", "-f", "-")
	check.Stdin = strings.NewReader(table)
	if e = check.Run(); e != nil {
		return errors.New("透明防火墙校验失败，未应用")
	}
	rules, e := run(ctx, "ip", "rule", "show")
	if e != nil {
		return errors.New("透明路由读取失败")
	}
	if !strings.Contains(string(rules), "fwmark 0x77 lookup 177") {
		if _, e = run(ctx, "ip", "rule", "add", "priority", "177", "fwmark", "0x77", "lookup", "177"); e != nil {
			return errors.New("透明路由规则设置失败")
		}
	}
	if _, e = run(ctx, "ip", "route", "replace", "local", "default", "dev", "lo", "table", "177"); e != nil {
		return errors.New("透明路由设置失败")
	}
	apply := exec.CommandContext(ctx, "nft", "-f", "-")
	apply.Stdin = strings.NewReader(table)
	if e = apply.Run(); e != nil {
		return errors.New("透明防火墙应用失败")
	}
	return a.resetCounters()
}
func (a *Agent) counters(ctx context.Context) map[string][2]int64 {
	out := map[string][2]int64{}
	data, e := run(ctx, "nft", "-j", "list", "table", "inet", "treeroot_home")
	if e != nil {
		return out
	}
	var obj struct {
		Nft []struct {
			Counter *struct {
				Name  string `json:"name"`
				Bytes int64  `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if json.Unmarshal(data, &obj) != nil {
		return out
	}
	for _, entry := range obj.Nft {
		if entry.Counter == nil {
			continue
		}
		name := entry.Counter.Name
		direction := 0
		key := strings.TrimPrefix(name, "up_")
		if strings.HasPrefix(name, "down_") {
			key = strings.TrimPrefix(name, "down_")
			direction = 1
		}
		if len(key) != 12 {
			continue
		}
		var m []string
		for i := 0; i < 12; i += 2 {
			m = append(m, key[i:i+2])
		}
		mac := strings.Join(m, ":")
		v := out[mac]
		v[direction] = entry.Counter.Bytes
		out[mac] = v
	}
	return out
}
