package homeap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedSessionAndMACBeforeMutation(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Error("missing firmware headers")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			json.NewEncoder(w).Encode(map[string]any{"error_code": 0, "stok": "abc%2B%2F%3D"})
			return
		}
		if !strings.HasSuffix(r.RequestURI, "/stok=abc%2B%2F%3D/ds") {
			t.Errorf("session encoded incorrectly: %s", r.RequestURI)
		}
		if body["method"] == "set" {
			writes++
		}
		json.NewEncoder(w).Encode(map[string]any{"error_code": 0, "network": map[string]any{"lan": map[string]any{"macaddr": "24-5a-5f-93-a5-71"}}})
	}))
	defer server.Close()
	client := &TPLink{Base: server.URL, Client: server.Client(), Password: "fixture-only", MAC: "24:5a:5f:93:a5:71"}
	if e := client.Login(context.Background()); e != nil {
		t.Fatal(e)
	}
	client.MAC = "24:5a:5f:93:a5:72"
	if e := client.Login(context.Background()); e == nil {
		t.Fatal("accepted different AP")
	}
	if _, e := client.Request(context.Background(), map[string]any{"method": "set"}); e == nil {
		t.Fatal("identity mismatch retained usable session")
	}
	if writes != 0 {
		t.Fatal("wrote during identity check")
	}
}

func fixtureWireless() map[string]any {
	return map[string]any{"wireless": map[string]any{
		"wlan_bs":      map[string]any{"bs_enable": "1", "wifi_enable": "1", "ssid": "fixture", "key": "private-fixture-key", "encryption": "1"},
		"wlan_host_2g": map[string]any{"enable": "1", "ssid": "fixture", "key": "private-fixture-key", "encryption": "1", "channel": "0"},
		"wlan_host_5g": map[string]any{"enable": "1", "ssid": "fixture", "key": "private-fixture-key", "encryption": "1", "channel": "0"},
	}}
}
func TestStatusNeverContainsWiFiCredentials(t *testing.T) {
	view := View(fixtureWireless())
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "private-fixture-key") || strings.Contains(string(raw), "\"key\"") {
		t.Fatal("credential in status")
	}
	if !view.Online || !view.Unified || !view.Band2.Secured || view.GuestSupported {
		t.Fatal(view)
	}
}
func TestWirelessValidationAndPreservedKey(t *testing.T) {
	s := Settings{Unified: true, Band2: Band{Enabled: true, SSID: "next", Channel: 0}, Band5: Band{Enabled: true, SSID: "next", Channel: 0}}
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"error_code":0}`))
	}))
	defer server.Close()
	tplink := TPLink{Client: server.Client(), endpoint: server.URL}
	if e := tplink.Set(context.Background(), s, fixtureWireless()); e != nil {
		t.Fatal(e)
	}
	if section(got, "wireless", "wlan_bs")["key"] != SessionPath("private-fixture-key") {
		t.Fatal("empty field did not preserve key")
	}
	before := fixtureWireless()
	section(before, "wireless", "wlan_bs")["key"] = ""
	if e := tplink.Set(context.Background(), s, before); e == nil {
		t.Fatal("created open AP")
	}
	s.Key2 = "short"
	if e := s.Validate(); e == nil {
		t.Fatal("accepted short key")
	}
	s.Key2 = ""
	s.Band5.Channel = 14
	if e := s.Validate(); e == nil {
		t.Fatal("accepted unsupported 5G channel")
	}
}

func TestFirmwareJSONStringCodec(t *testing.T) {
	value := "家庭 Wi-Fi + 100% !'()*"
	encoded := SessionPath(value)
	if strings.Contains(encoded, " ") || strings.Contains(encoded, "+") || !strings.Contains(encoded, "!'()*") {
		t.Fatal(encoded)
	}
	wire := map[string]any{"name": []any{value}, "number": 1}
	transformStrings(wire, true)
	transformStrings(wire, false)
	if wire["name"].([]any)[0] != value || wire["number"] != 1 {
		t.Fatal("codec did not roundtrip firmware values")
	}
}
