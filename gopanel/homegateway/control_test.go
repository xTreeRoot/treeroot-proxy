package homegateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlChannelUsesBearerAndRejectsUntrustedTLS(t *testing.T) {
	seen := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.URL.Path != "/api/home/test/sync" || r.Header.Get("Authorization") != "Bearer private-test-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("management authentication or path mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"accepted":1}`))
	}))
	defer server.Close()
	a := &Agent{Config: AgentConfig{Server: server.URL, ID: "test", Token: "private-test-token"}, client: server.Client()}
	var reply Reply
	if e := a.serverRequest(context.Background(), "/sync", Sync{Sequence: 1}, &reply); e != nil || reply.Accepted != 1 || !seen {
		t.Fatal(e, reply)
	}
	seen = false
	a.client = &http.Client{}
	if e := a.serverRequest(context.Background(), "/sync", Sync{Sequence: 2}, &reply); e == nil || seen {
		t.Fatal("untrusted server certificate accepted")
	}
}
func TestDenyLogFromRealMihomoFormat(t *testing.T) {
	event, ok := ParseLog("[TCP] dial REJECT (match And/((SrcIPCIDR,192.168.77.101/32),(DomainSuffix,example.com))) 192.168.77.101:54321 --> example.com:443 error: reject")
	if !ok || event.Action != "reject" || event.Source != "192.168.77.101" || event.Port != 443 {
		t.Fatal(event, ok)
	}
}
func TestBindingChangesAreIndependentOfControlPlane(t *testing.T) {
	c := fixture()
	a := []Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.101"}}
	b := []Device{{MAC: c.Devices[0].MAC, IP: "192.168.77.104"}}
	if sameBindings(c, a, b) {
		t.Fatal("observed DHCP move hidden by reservation")
	}
	c.Devices[0].FixedIP = ""
	if sameBindings(c, a, b) {
		t.Fatal("DHCP move not detected")
	}
}

func TestReservationDoesNotMoveDeviceBeforeLeaseRenewal(t *testing.T) {
	c := fixture()
	mac := c.Devices[0].MAC
	d := bindPolicies(c, []Device{{MAC: mac, IP: "192.168.77.104"}})
	if len(d) != 1 || d[0].IP != "192.168.77.104" {
		t.Fatal("reservation replaced live address", d)
	}
	d = bindPolicies(c, []Device{{MAC: "52:54:00:77:01:02", IP: c.Devices[0].FixedIP}})
	if len(d) != 1 {
		t.Fatal("reservation claimed peer address", d)
	}
}
