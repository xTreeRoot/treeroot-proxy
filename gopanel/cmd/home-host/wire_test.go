package main

import (
	"strings"
	"testing"
)

func TestPhysicalRelayHasPrivateReconnectingEndpoint(t *testing.T) {
	dir := "/Users/a gateway/private/runtime"
	endpoint := strings.Join(wireEndpointArgs(dir), " ")
	relay := strings.Join(relayArgs(dir), " ")
	if !strings.Contains(endpoint, "addr.path="+dir+"/run/ethernet.sock,server=on") || !strings.Contains(endpoint, "hubid=77,netdev=physicalwire") {
		t.Fatal(endpoint)
	}
	if !strings.Contains(relay, "server=off,reconnect-ms=1000") || !strings.Contains(relay, "vmnet-bridged,id=physical,ifname=en0") {
		t.Fatal(relay)
	}
	for _, forbidden := range []string{"ifname=en1", "hostfwd", "-drive", "addr.type=inet", "listen="} {
		if strings.Contains(relay, forbidden) {
			t.Fatalf("relay acquired an unrelated interface, disk, or public control port: %s", forbidden)
		}
	}
}
