// Package homegateway defines the authenticated, structured home control protocol.
package homegateway

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

const Version = "v2026.11.25.home.3"

type Device struct {
	MAC          string   `json:"mac"`
	Name         string   `json:"name"`
	IP           string   `json:"ip"`
	IPv6         []string `json:"ipv6,omitempty"`
	Online       bool     `json:"online"`
	LastSeen     int64    `json:"last_seen"`
	LeaseExpires int64    `json:"lease_expires"`
	Upload       int64    `json:"upload"`
	Download     int64    `json:"download"`
}
type DevicePolicy struct {
	MAC        string `json:"mac"`
	Name       string `json:"name"`
	Note       string `json:"note"`
	Group      string `json:"group"`
	FixedIP    string `json:"fixed_ip"`
	Blocked    bool   `json:"blocked"`
	SpeedBPS   int64  `json:"speed_bps"`
	TotalBytes int64  `json:"total_bytes"`
	DailyBytes int64  `json:"daily_bytes"`
}
type Rule struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Subject    string `json:"subject"`
	TargetType string `json:"target_type"`
	Target     string `json:"target"`
	Port       string `json:"port"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
	Node       string `json:"node"`
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	NoResolve  bool   `json:"no_resolve,omitempty"`
}
type Node struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Proxy map[string]any `json:"proxy,omitempty"`
}
type Config struct {
	Revision    int64          `json:"revision"`
	Default     string         `json:"default"`
	DefaultNode string         `json:"default_node"`
	Devices     []DevicePolicy `json:"devices"`
	Rules       []Rule         `json:"rules"`
	Nodes       []Node         `json:"nodes"`
}
type Capabilities struct {
	Routing  bool   `json:"routing"`
	DHCP     bool   `json:"dhcp"`
	Limits   bool   `json:"limits"`
	WiFi     bool   `json:"wifi"`
	APReboot bool   `json:"ap_reboot"`
	Upgrade  bool   `json:"upgrade"`
	IPv6     string `json:"ipv6"`
	Mode     string `json:"mode"`
}
type Event struct {
	ID       string `json:"id"`
	TS       int64  `json:"ts"`
	MAC      string `json:"mac"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Rule     string `json:"rule"`
	Action   string `json:"action"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Outcome  string `json:"outcome,omitempty"`
}
type Task struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Created int64             `json:"created"`
	Expires int64             `json:"expires"`
	Payload map[string]string `json:"payload,omitempty"`
}
type Result struct {
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}
type Sync struct {
	Boot         string         `json:"boot"`
	Sequence     int64          `json:"sequence"`
	Version      string         `json:"version"`
	Applied      int64          `json:"applied"`
	Error        string         `json:"error"`
	Status       map[string]any `json:"status"`
	Capabilities Capabilities   `json:"capabilities"`
	Devices      []Device       `json:"devices"`
	Events       []Event        `json:"events"`
	Results      []Result       `json:"results"`
	Traffic      *TrafficSample `json:"traffic,omitempty"`
}
type Reply struct {
	Accepted    int64   `json:"accepted"`
	Config      *Config `json:"config,omitempty"`
	ConfigError string  `json:"config_error,omitempty"`
	Tasks       []Task  `json:"tasks"`
}

var labelPattern = regexp.MustCompile(`^[a-zA-Z0-9_. -]{0,64}$`)
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func MAC(value string) (string, error) {
	m, e := net.ParseMAC(value)
	if e != nil || len(m) != 6 || m[0]&1 != 0 {
		return "", errors.New("设备 MAC 地址无效")
	}
	return m.String(), nil
}
func (c Config) Validate() error {
	if c.Default != "direct" && c.Default != "proxy" && c.Default != "reject" {
		return errors.New("默认策略无效")
	}
	if len(c.Devices) > 256 || len(c.Rules) > 512 || len(c.Nodes) > 32 {
		return errors.New("配置超过容量限制")
	}
	nodes := map[string]bool{}
	for _, n := range c.Nodes {
		if !labelPattern.MatchString(n.ID) || n.ID == "" || nodes[n.ID] {
			return errors.New("节点标识无效")
		}
		nodes[n.ID] = true
	}
	if c.Default == "proxy" && !nodes[c.DefaultNode] {
		return errors.New("请选择默认代理节点")
	}
	macs := map[string]bool{}
	ips := map[string]bool{}
	for _, d := range c.Devices {
		m, e := MAC(d.MAC)
		if e != nil {
			return e
		}
		if m != d.MAC || macs[m] {
			return errors.New("设备身份重复或未规范化")
		}
		macs[m] = true
		if len(d.Name) > 192 || len(d.Note) > 1024 || len(d.Group) > 128 || strings.ContainsAny(d.Name, "\n\r") {
			return errors.New("设备字段过长或无效")
		}
		if d.FixedIP != "" {
			ip, e := netip.ParseAddr(d.FixedIP)
			if e != nil || !netip.MustParsePrefix("192.168.77.0/24").Contains(ip) || ip.String() == "192.168.77.1" || ip.String() == "192.168.77.2" || ip.String() == "192.168.77.0" || ip.String() == "192.168.77.255" || ips[ip.String()] {
				return errors.New("固定地址必须是 LAN 中未重复的可用地址")
			}
			ips[ip.String()] = true
		}
		if d.SpeedBPS < 0 || d.SpeedBPS > 100_000_000_000 || d.TotalBytes < 0 || d.DailyBytes < 0 {
			return errors.New("限额或速率无效")
		}
	}
	ids := map[string]bool{}
	for _, r := range c.Rules {
		if r.ID == "" || len(r.ID) > 64 || ids[r.ID] {
			return errors.New("规则标识重复或无效")
		}
		ids[r.ID] = true
		if r.Scope != "all" && r.Scope != "device" && r.Scope != "group" {
			return errors.New("规则范围无效")
		}
		if r.Scope == "device" {
			if _, e := MAC(r.Subject); e != nil {
				return e
			}
		}
		if r.Scope == "group" && (r.Subject == "" || len(r.Subject) > 128) {
			return errors.New("设备组无效")
		}
		switch r.TargetType {
		case "any":
			if r.Target != "" {
				return errors.New("全部目标不应填写地址")
			}
		case "domain", "suffix":
			if !domainPattern.MatchString(strings.ToLower(r.Target)) || len(r.Target) > 253 {
				return errors.New("域名无效，请填写不含协议的域名")
			}
		case "keyword":
			if len(r.Target) == 0 || len(r.Target) > 253 || !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(r.Target) {
				return errors.New("域名关键字无效")
			}
		case "ip":
			if _, e := netip.ParseAddr(r.Target); e != nil {
				return errors.New("IP 地址无效")
			}
		case "cidr":
			if _, e := netip.ParsePrefix(r.Target); e != nil {
				return errors.New("网段无效")
			}
		case "geoip":
			if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(r.Target) {
				return errors.New("GEOIP 须使用两位大写国家代码")
			}
		default:
			return errors.New("目标类型无效")
		}
		if r.NoResolve && r.TargetType != "cidr" && r.TargetType != "ip" && r.TargetType != "geoip" {
			return errors.New("no-resolve 只适用于 IP、网段和 GEOIP")
		}
		if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" {
			return errors.New("传输协议无效")
		}
		if r.Port != "" {
			var lo, hi int
			if strings.Contains(r.Port, "-") {
				if _, e := fmt.Sscanf(r.Port, "%d-%d", &lo, &hi); e != nil {
					return errors.New("端口无效")
				}
			} else {
				if _, e := fmt.Sscanf(r.Port, "%d", &lo); e != nil {
					return errors.New("端口无效")
				}
				hi = lo
			}
			if lo < 1 || hi > 65535 || lo > hi || !regexp.MustCompile(`^[0-9]+(-[0-9]+)?$`).MatchString(r.Port) {
				return errors.New("端口范围无效")
			}
		}
		if r.Priority < 0 || r.Priority > 99999 {
			return errors.New("优先级无效")
		}
		if r.Action != "direct" && r.Action != "proxy" && r.Action != "reject" {
			return errors.New("动作无效")
		}
		if r.Action == "proxy" && !nodes[r.Node] {
			return errors.New("规则节点不存在")
		}
	}
	return nil
}

// CompileRules places all denials ahead of routing choices, so selecting a node cannot bypass them.
func (c Config) CompileRules(devices []Device) ([]string, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	rules := append([]Rule(nil), c.Rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		if (rules[i].Action == "reject") != (rules[j].Action == "reject") {
			return rules[i].Action == "reject"
		}
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return false
	})
	groups := map[string]string{}
	for _, d := range c.Devices {
		groups[d.MAC] = d.Group
	}
	var result []string
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		var sources []string
		if r.Scope == "all" {
			sources = []string{""}
		} else {
			for _, d := range devices {
				if (r.Scope == "device" && r.Subject == d.MAC) || (r.Scope == "group" && r.Subject == groups[d.MAC]) {
					if ip, e := netip.ParseAddr(d.IP); e == nil && ip.Is4() {
						sources = append(sources, ip.String()+"/32")
					}
				}
			}
		}
		for _, src := range sources {
			var parts []string
			if src != "" {
				parts = append(parts, "(SRC-IP-CIDR,"+src+")")
			}
			switch r.TargetType {
			case "domain":
				parts = append(parts, "(DOMAIN,"+strings.ToLower(r.Target)+")")
			case "suffix":
				parts = append(parts, "(DOMAIN-SUFFIX,"+strings.ToLower(r.Target)+")")
			case "keyword":
				parts = append(parts, "(DOMAIN-KEYWORD,"+strings.ToLower(r.Target)+")")
			case "ip":
				ip, _ := netip.ParseAddr(r.Target)
				parts = append(parts, fmt.Sprintf("(IP-CIDR,%s/%d)", ip, ip.BitLen()))
			case "cidr":
				parts = append(parts, "(IP-CIDR,"+r.Target+")")
			case "geoip":
				parts = append(parts, "(GEOIP,"+r.Target+")")
			}
			if r.NoResolve && len(parts) > 0 {
				last := len(parts) - 1
				parts[last] = strings.TrimSuffix(parts[last], ")") + ",no-resolve)"
			}
			if r.Port != "" {
				parts = append(parts, "(DST-PORT,"+r.Port+")")
			}
			if r.Protocol != "" {
				parts = append(parts, "(NETWORK,"+r.Protocol+")")
			}
			target := map[string]string{"direct": "DIRECT", "reject": "REJECT", "proxy": "HG-" + r.Node}[r.Action]
			if len(parts) == 0 {
				result = append(result, "MATCH,"+target)
			} else if len(parts) == 1 {
				part := strings.Trim(parts[0], "()")
				if r.NoResolve {
					part = strings.TrimSuffix(part, ",no-resolve")
				}
				line := part + "," + target
				if r.NoResolve {
					line += ",no-resolve"
				}
				result = append(result, line)
			} else {
				result = append(result, "AND,("+strings.Join(parts, ",")+"),"+target)
			}
		}
	}
	target := map[string]string{"direct": "DIRECT", "reject": "REJECT", "proxy": "HG-" + c.DefaultNode}[c.Default]
	return append(result, "MATCH,"+target), nil
}
