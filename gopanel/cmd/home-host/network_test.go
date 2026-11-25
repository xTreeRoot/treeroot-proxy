package main

import (
	"strings"
	"testing"
)

func TestFixedEthernetBaseline(t *testing.T) {
	s := networkSnapshot{EthernetEnabled: "Enabled\n", Ethernet: "Manual Configuration\nIP address: 192.168.77.2\nSubnet mask: 255.255.255.0\nRouter: 0.0.0.0\nIPv6: Off\n"}
	if !fixedEthernetBaseline(s) {
		t.Fatal("authorized fixed LAN rejected")
	}
	for _, change := range [][2]string{{"192.168.77.2", "192.168.77.20"}, {"0.0.0.0", "192.168.77.1"}, {"IPv6: Off", "IPv6: Automatic"}, {"255.255.255.0", "255.255.0.0"}} {
		modified := s
		modified.Ethernet = strings.ReplaceAll(modified.Ethernet, change[0], change[1])
		if fixedEthernetBaseline(modified) {
			t.Fatal("unexpected network baseline accepted", change)
		}
	}
}

func TestRouterSessionURL(t *testing.T) {
	if got := routerSessionPath("a+b/c= d"); got != "a%2Bb%2Fc%3D%20d" {
		t.Fatal("session URL differs from firmware encoding", got)
	}
}
