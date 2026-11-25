package homegateway

import (
	"reflect"
	"strings"
	"testing"
)

const userRulePatch = `prepend:
  - 'DOMAIN-KEYWORD,treeroot.top,DIRECT'
  - 'DOMAIN-KEYWORD,manxing.net,DIRECT'
  - 'DOMAIN,myxq.zhient.com,DIRECT'
  - 'DOMAIN,p3.music.126.net,DIRECT'
  - 'DOMAIN,zhient.com,DIRECT'
append: []
delete: []
`

func TestAdvancedUserPatchIsScopedAndCannotBypassDenial(t *testing.T) {
	c := fixture()
	c.Rules = []Rule{{ID: "deny", Scope: "device", Subject: c.Devices[0].MAC, TargetType: "domain", Target: "myxq.zhient.com", Action: "reject", Enabled: true, Priority: 99999}}
	p, e := PreviewRules(c, userRulePatch, "device", c.Devices[0].MAC, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Changes) != 5 || len(p.Config.Rules) != 6 {
		t.Fatal(p)
	}
	if c.Rules[0].Priority != 99999 {
		t.Fatal("preview mutated original configuration")
	}
	compiled, e := p.Config.CompileRules([]Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.101"}, {MAC: "52:54:00:77:01:02", IP: "192.168.77.102"}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(compiled[0], ",REJECT") || !strings.Contains(compiled[1], "DOMAIN-KEYWORD,treeroot.top") || strings.Contains(strings.Join(compiled[:6], "\n"), ".102") {
		t.Fatal(compiled)
	}
	second, e := PreviewRules(p.Config, userRulePatch, "device", c.Devices[0].MAC, nil)
	if e != nil || len(second.Changes) != 0 || !reflect.DeepEqual(second.Config, p.Config) {
		t.Fatal("reimport was not idempotent", second, e)
	}
}

func TestAdvancedPrependAppendDeleteAndScope(t *testing.T) {
	c := fixture()
	c.Rules = []Rule{
		{ID: "same-a", Scope: "device", Subject: c.Devices[0].MAC, TargetType: "domain", Target: "example.com", Action: "direct", Enabled: true, Priority: 10},
		{ID: "same-b", Scope: "device", Subject: "52:54:00:77:01:02", TargetType: "domain", Target: "example.com", Action: "direct", Enabled: true, Priority: 10},
	}
	p, e := PreviewRules(c, "prepend: ['DOMAIN-KEYWORD,first,DIRECT']\nappend: ['IP-CIDR,1.1.1.0/24,vps1']\ndelete: ['DOMAIN,example.com,DIRECT']", "device", c.Devices[0].MAC, c.Nodes)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Changes) != 3 || len(p.Config.Rules) != 3 || p.Config.Rules[0].Target != "first" || p.Config.Rules[1].ID != "same-b" || p.Config.Rules[2].Node != "vps1" {
		t.Fatal(p)
	}
}

func TestAdvancedRejectsMalformedAndUnsupportedInputAtomically(t *testing.T) {
	for _, source := range []string{
		"prepend: ['DOMAIN-KEYWORD,a),REJECT,DIRECT']",
		"prepend: ['DOMAIN,example.com,unknown-node']",
		"prepend: ['IP-CIDR,not-an-ip,DIRECT']",
		"prepend: ['SCRIPT,payload,DIRECT']",
		"prepend: ['DOMAIN,example.com,DIRECT,no-resolve']",
		"prepend: ['DOMAIN,example.com,DIRECT']\nextra: true",
		"prepend: ['DOMAIN,example.com,DIRECT']\n---\nappend: []",
		"prepend: [1]",
		"prepend: ['DOMAIN,example.com,DIRECT'",
	} {
		t.Run(source, func(t *testing.T) {
			c := fixture()
			before := c
			if _, e := PreviewRules(c, source, "all", "", c.Nodes); e == nil {
				t.Fatal("accepted invalid patch")
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("failed preview changed configuration")
			}
		})
	}
}

func TestGeoIPNoResolveCompilesAndKeepsDistinctImport(t *testing.T) {
	c := fixture()
	p, e := PreviewRules(c, "prepend: ['GEOIP,CN,DIRECT,no-resolve', 'IP-CIDR,10.0.0.0/8,DIRECT,no-resolve']", "all", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	rules, e := p.Config.CompileRules(nil)
	if e != nil || rules[0] != "GEOIP,CN,DIRECT,no-resolve" || rules[1] != "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve" {
		t.Fatal(rules, e)
	}
	scoped, e := PreviewRules(c, "prepend: ['GEOIP,CN,DIRECT,no-resolve']", "device", c.Devices[0].MAC, nil)
	if e != nil {
		t.Fatal(e)
	}
	rules, e = scoped.Config.CompileRules([]Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.101"}})
	if e != nil || !strings.Contains(rules[0], "(GEOIP,CN,no-resolve)") {
		t.Fatal(rules, e)
	}
	different, e := PreviewRules(p.Config, "prepend: ['GEOIP,CN,DIRECT']", "all", "", nil)
	if e != nil || len(different.Changes) != 1 {
		t.Fatal(different, e)
	}
}
