package homegateway

import (
	"strings"
	"testing"
)

func fixture() Config {
	return Config{Revision: 1, Default: "direct", Devices: []DevicePolicy{{MAC: "52:54:00:77:01:01", Group: "kids", FixedIP: "192.168.77.101"}}, Nodes: []Node{{ID: "vps1"}}}
}
func TestDenyBeforeRoutingAndSourceScope(t *testing.T) {
	c := fixture()
	c.Rules = []Rule{{ID: "route", Scope: "all", TargetType: "any", Action: "proxy", Node: "vps1", Enabled: true}, {ID: "deny", Scope: "device", Subject: c.Devices[0].MAC, TargetType: "suffix", Target: "example.com", Action: "reject", Priority: 99999, Enabled: true}}
	r, e := c.CompileRules([]Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.101"}, {MAC: "52:54:00:77:01:02", IP: "192.168.77.102"}})
	if e != nil {
		t.Fatal(e)
	}
	want := "AND,((SRC-IP-CIDR,192.168.77.101/32),(DOMAIN-SUFFIX,example.com)),REJECT"
	if r[0] != want {
		t.Fatalf("%q", r)
	}
	if strings.Contains(r[0], ".102") {
		t.Fatal("B included in A denial")
	}
	c.Rules[1].Enabled = false
	r, e = c.CompileRules(nil)
	if e != nil || strings.Contains(strings.Join(r, "\n"), "REJECT") {
		t.Fatal("disabled rule still compiled")
	}
}
func TestGroupPortsIPv6AndWithdraw(t *testing.T) {
	c := fixture()
	c.Rules = []Rule{{ID: "v6", Scope: "group", Subject: "kids", TargetType: "cidr", Target: "2001:db8::/32", Protocol: "udp", Port: "443-8443", Action: "reject", Enabled: true}}
	r, e := c.CompileRules([]Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.101"}})
	if e != nil {
		t.Fatal(e)
	}
	for _, part := range []string{"SRC-IP-CIDR,192.168.77.101/32", "IP-CIDR,2001:db8::/32", "DST-PORT,443-8443", "NETWORK,udp"} {
		if !strings.Contains(r[0], part) {
			t.Fatal(r)
		}
	}
	c.Rules = nil
	r, e = c.CompileRules(nil)
	if e != nil || len(r) != 1 || r[0] != "MATCH,DIRECT" {
		t.Fatal(r, e)
	}
}
func TestRejectUntrustedFirewallAddresses(t *testing.T) {
	c := fixture()
	for _, ip := range []string{"1.1.1.1", "192.168.77.101 counter accept", "::1"} {
		if _, e := Firewall(c, []Device{{MAC: c.Devices[0].MAC, IP: ip}}, nil); e == nil {
			t.Fatalf("accepted %q", ip)
		}
	}
}
func TestBadConfigurations(t *testing.T) {
	for _, target := range []string{"example.com),DIRECT", "https://example.com", "foo\nbar", "*.example.com"} {
		c := fixture()
		c.Rules = []Rule{{ID: "r", Scope: "all", TargetType: "domain", Target: target, Action: "reject", Enabled: true}}
		if c.Validate() == nil {
			t.Fatal(target)
		}
	}
	c := fixture()
	c.Devices = append(c.Devices, c.Devices[0])
	if c.Validate() == nil {
		t.Fatal("duplicate MAC")
	}
}
func TestQuotaAndDNSOrder(t *testing.T) {
	c := fixture()
	c.Devices[0].TotalBytes = 100
	c.Devices[0].DailyBytes = 80
	s, e := Firewall(c, []Device{{MAC: c.Devices[0].MAC, IP: c.Devices[0].FixedIP}}, map[string][2]int64{c.Devices[0].MAC: {50, 20}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(s, "over 50 bytes") {
		t.Fatal(s)
	}
	transparent := strings.Split(s, "chain transparent")[1]
	if strings.Index(transparent, "tcp dport 53 return") > strings.Index(transparent, "tproxy to") {
		t.Fatal("DNS captured before redirect")
	}
}

func TestConnectionLogDoesNotForwardArbitraryLogs(t *testing.T) {
	e, ok := ParseLog("[TCP] 192.168.77.101:50234 --> example.com:443 match Domain(example.com) using REJECT")
	if !ok || e.Source != "192.168.77.101" || e.Target != "example.com" || e.Action != "reject" {
		t.Fatal(e, ok)
	}
	for _, raw := range []string{"configuration secret=private", "RESTful API listening at 127.0.0.1:9090", "[TCP] malformed"} {
		if _, ok := ParseLog(raw); ok {
			t.Fatal("forwarded non-connection log")
		}
	}
}
