// Package homeap controls only the verified TP-LINK stock-firmware protocol.
package homeap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Encode is the obfuscation used by the fetched firmware JavaScript, not a
// password hash. All callers must keep both input and output private.
func Encode(password string) string {
	key := "RDpbLfCPsJZ7fiv"
	alphabet := "yLwVl0zKqws7LgKPRQ84Mdt708T1qQ3Ha7xv3H7NyU84p21BriUWBU43odz3iP4rBL3cD02KZciXTysVXiV8ngg6vL48rPJyAUw0HurW20xqxv9aYb4M9wK1Ae0wlro510qXeU07kV57fQMc8L6aLgMLwygtc0F10a0Dg70TOoouyFhdysuRMO51yY5ZlOZZLEal1h0t9YQW0Ko7oBwmCAHoic4HYbUyVeU3sfQ1xtXcPcf1aT303wAQhv66qzW"
	var b strings.Builder
	for i := 0; i < max(len(password), len(key)); i++ {
		x, y := byte(187), byte(187)
		if i < len(key) {
			x = key[i]
		}
		if i < len(password) {
			y = password[i]
		}
		b.WriteByte(alphabet[int(x^y)%len(alphabet)])
	}
	return b.String()
}

func SessionPath(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, v := range []byte(value) {
		if v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || strings.ContainsRune("-_.!~*'()", rune(v)) {
			b.WriteByte(v)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[v>>4])
			b.WriteByte(hex[v&15])
		}
	}
	return b.String()
}
func transformStrings(v any, encode bool) any {
	switch value := v.(type) {
	case string:
		if encode {
			return SessionPath(value)
		}
		if decoded, e := url.PathUnescape(value); e == nil {
			return decoded
		}
		return value
	case map[string]any:
		for k, item := range value {
			value[k] = transformStrings(item, encode)
		}
	case []any:
		for i, item := range value {
			value[i] = transformStrings(item, encode)
		}
	}
	return v
}

func JSON(ctx context.Context, client *http.Client, target string, body any) (map[string]any, error) {
	raw, e := json.Marshal(body)
	if e != nil {
		return nil, e
	}
	var wire any
	if e = json.Unmarshal(raw, &wire); e != nil {
		return nil, e
	}
	raw, e = json.Marshal(transformStrings(wire, true))
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	u, e := url.Parse(target)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	res, e := client.Do(req)
	if e != nil {
		return nil, errors.New("AP 管理请求未响应")
	}
	defer res.Body.Close()
	var reply map[string]any
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&reply) != nil {
		return nil, errors.New("AP 返回格式无效")
	}
	transformStrings(reply, false)
	if res.StatusCode != 200 || reply["error_code"] != float64(0) {
		return nil, fmt.Errorf("AP 拒绝请求（HTTP %d，代码 %v）", res.StatusCode, reply["error_code"])
	}
	return reply, nil
}

type TPLink struct {
	Client              *http.Client
	Base, Password, MAC string
	endpoint            string
}

func (t *TPLink) Login(ctx context.Context) error {
	v, e := JSON(ctx, t.Client, t.Base+"/", map[string]any{"method": "do", "login": map[string]string{"password": Encode(t.Password)}})
	if e != nil {
		return e
	}
	stok, ok := v["stok"].(string)
	if !ok || stok == "" {
		return errors.New("AP 未确认登录")
	}
	t.endpoint = t.Base + "/stok=" + SessionPath(stok) + "/ds"
	v, e = t.Request(ctx, map[string]any{"method": "get", "network": map[string]any{"name": "lan"}})
	if e != nil {
		return e
	}
	lan := section(v, "network", "lan")
	mac, e := net.ParseMAC(strings.ReplaceAll(stringValue(lan["macaddr"]), "-", ":"))
	want, e2 := net.ParseMAC(t.MAC)
	if e != nil || e2 != nil || mac.String() != want.String() {
		t.endpoint = ""
		return errors.New("AP 身份不符，未修改设置")
	}
	return nil
}
func (t *TPLink) Request(ctx context.Context, body any) (map[string]any, error) {
	if t.endpoint == "" {
		return nil, errors.New("AP 尚未认证")
	}
	return JSON(ctx, t.Client, t.endpoint, body)
}
func section(v map[string]any, module, name string) map[string]any {
	m, _ := v[module].(map[string]any)
	s, _ := m[name].(map[string]any)
	return s
}
func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func enabled(v any) bool { return stringValue(v) == "1" }

type Band struct {
	Enabled bool   `json:"enabled"`
	SSID    string `json:"ssid"`
	Secured bool   `json:"secured"`
	Channel int    `json:"channel"`
}
type WiFi struct {
	Online         bool `json:"online"`
	Unified        bool `json:"unified"`
	Band2          Band `json:"band2"`
	Band5          Band `json:"band5"`
	GuestSupported bool `json:"guest_supported"`
}
type Settings struct {
	Unified bool   `json:"unified"`
	Band2   Band   `json:"band2"`
	Band5   Band   `json:"band5"`
	Key2    string `json:"key2,omitempty"`
	Key5    string `json:"key5,omitempty"`
}

func (s Settings) Validate() error {
	for _, b := range []Band{s.Band2, s.Band5} {
		if len(b.SSID) < 1 || len([]byte(b.SSID)) > 32 || strings.ContainsAny(b.SSID, "\r\n\x00") {
			return errors.New("Wi-Fi 名称须为 1–32 字节")
		}
	}
	for _, key := range []string{s.Key2, s.Key5} {
		if key != "" {
			if len(key) < 8 || len(key) > 63 {
				return errors.New("Wi-Fi 密码须为 8–63 个字符")
			}
			for _, r := range key {
				if r < 32 || r > 126 {
					return errors.New("Wi-Fi 密码请使用英文、数字或符号")
				}
			}
		}
	}
	if s.Band2.Channel < 0 || s.Band2.Channel > 13 {
		return errors.New("2.4G 信道无效")
	}
	valid5 := map[int]bool{0: true, 36: true, 40: true, 44: true, 48: true, 149: true, 153: true, 157: true, 161: true, 165: true}
	if !valid5[s.Band5.Channel] {
		return errors.New("5G 信道无效")
	}
	return nil
}
func (t *TPLink) Snapshot(ctx context.Context) (map[string]any, error) {
	return t.Request(ctx, map[string]any{"method": "get", "wireless": map[string]any{"name": []string{"wlan_host_2g", "wlan_host_5g", "wlan_bs"}}})
}
func View(raw map[string]any) WiFi {
	bs := section(raw, "wireless", "wlan_bs")
	view := WiFi{Online: true, Unified: enabled(bs["bs_enable"])}
	band := func(name string) Band {
		v := section(raw, "wireless", name)
		channel, _ := strconv.Atoi(stringValue(v["channel"]))
		return Band{enabled(v["enable"]), stringValue(v["ssid"]), enabled(v["encryption"]), channel}
	}
	view.Band2, view.Band5 = band("wlan_host_2g"), band("wlan_host_5g")
	if view.Unified {
		for _, b := range []*Band{&view.Band2, &view.Band5} {
			b.Enabled = enabled(bs["wifi_enable"])
			b.SSID = stringValue(bs["ssid"])
			b.Secured = enabled(bs["encryption"])
		}
	}
	return view
}
func Matches(raw map[string]any, s Settings) bool {
	v := View(raw)
	match := func(got, w Band) bool {
		return got.Enabled == w.Enabled && got.SSID == w.SSID && got.Channel == w.Channel && got.Secured
	}
	if v.Unified != s.Unified || !match(v.Band2, s.Band2) || (!s.Unified && !match(v.Band5, s.Band5)) {
		return false
	}
	for _, p := range []struct{ key, section string }{{s.Key2, "wlan_host_2g"}, {s.Key5, "wlan_host_5g"}} {
		if p.key == "" {
			continue
		}
		name := p.section
		if s.Unified {
			name = "wlan_bs"
			if p.section == "wlan_host_5g" {
				continue
			}
		}
		if stringValue(section(raw, "wireless", name)["key"]) != p.key {
			return false
		}
	}
	return true
}
func (t *TPLink) Restore(ctx context.Context, raw map[string]any) error {
	wireless, _ := raw["wireless"].(map[string]any)
	if len(wireless) != 3 {
		return errors.New("AP 无线备份无效")
	}
	_, e := t.Request(ctx, map[string]any{"method": "set", "wireless": wireless})
	return e
}
func (t *TPLink) Set(ctx context.Context, s Settings, before map[string]any) error {
	if e := s.Validate(); e != nil {
		return e
	}
	wireless := map[string]any{}
	bit := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	current := View(before)
	keyFor := func(k, name string) string {
		if k != "" {
			return k
		}
		if current.Unified {
			return stringValue(section(before, "wireless", "wlan_bs")["key"])
		}
		return stringValue(section(before, "wireless", name)["key"])
	}
	k2, k5 := keyFor(s.Key2, "wlan_host_2g"), keyFor(s.Key5, "wlan_host_5g")
	if s.Unified {
		k5 = k2
	}
	// Empty form fields preserve the current key; never silently create an open AP.
	if len(k2) < 8 || (!s.Unified && len(k5) < 8) {
		return errors.New("请为 Wi-Fi 设置独立密码")
	}
	for _, p := range []struct {
		name string
		b    Band
		k    string
	}{{"wlan_host_2g", s.Band2, k2}, {"wlan_host_5g", s.Band5, k5}} {
		wireless[p.name] = map[string]any{"enable": bit(p.b.Enabled), "ssid": p.b.SSID, "key": p.k, "encryption": 1, "ssidbrd": 1, "channel": p.b.Channel}
	}
	wireless["wlan_bs"] = map[string]any{"bs_enable": bit(s.Unified), "wifi_enable": bit(s.Band2.Enabled), "ssid": s.Band2.SSID, "key": k2, "encryption": 1, "ssidbrd": 1}
	_, e := t.Request(ctx, map[string]any{"method": "set", "wireless": wireless})
	return e
}
func (t *TPLink) Reboot(ctx context.Context) error {
	_, e := t.Request(ctx, map[string]any{"method": "do", "system": map[string]any{"reboot": nil}})
	return e
}
