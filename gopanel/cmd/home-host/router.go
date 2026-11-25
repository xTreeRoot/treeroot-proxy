package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"treeroot/gopanel/homeap"
)

var routerPassword string

func readRouterPassword() (string, error) {
	mode := exec.Command("/bin/stty", "-echo")
	mode.Stdin = os.Stdin
	if e := mode.Run(); e != nil {
		return "", errors.New("需要可隐藏输入的本机终端")
	}
	defer func() { mode := exec.Command("/bin/stty", "echo"); mode.Stdin = os.Stdin; mode.Run(); fmt.Println() }()
	fmt.Print("AP 管理凭据（隐藏输入）：")
	line, e := bufio.NewReader(os.Stdin).ReadString('\n')
	if e != nil {
		return "", e
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// These values and request shapes are taken from this router's fetched
// /web-static/lib/jquery-1.10.1.js. They are protocol constants, not credentials.
func routerEncode(password string) string {
	return homeap.Encode(password)
}

type routerCredentials struct {
	Password string `json:"password"`
}

func checkRouterState(client *http.Client, base string) error {
	response, e := client.Get(base + "/pc/Content.htm")
	if e != nil {
		return errors.New("AP 状态页未响应")
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if e != nil {
		return e
	}
	var state struct {
		Data struct {
			Code int `json:"code"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &state) != nil {
		return errors.New("AP 状态格式待核实，未设置密码")
	}
	switch state.Data.Code {
	case -40405:
		fmt.Println("AP reports factory-reset state; administrator password has not been initialized")
	case -40407:
		fmt.Println("AP reports normal unauthenticated client state; factory reset is not confirmed")
	default:
		fmt.Printf("AP authentication state code=%d; no settings were changed\n", state.Data.Code)
	}
	path := filepath.Join(home, "private/ap-reset-state.html")
	if e = os.WriteFile(path, data, 0600); e != nil {
		return e
	}
	ownProbe(path)
	return nil
}

func routerJSON(client *http.Client, target string, body any) (map[string]any, error) {
	return homeap.JSON(context.Background(), client, target, body)
}

func inspectRouter(client *http.Client, base string, initialize bool) error {
	path := filepath.Join(home, "private", "ap-admin.json")
	var credentials routerCredentials
	raw, e := os.ReadFile(path)
	if e == nil {
		if json.Unmarshal(raw, &credentials) != nil {
			return errors.New("AP 私密配置损坏")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if routerPassword != "" {
		credentials.Password = routerPassword
	}
	if credentials.Password == "" || initialize {
		if !initialize {
			return errors.New("AP 尚未保存管理凭据")
		}
		res, e := client.Get(base + "/pc/Content.htm")
		if e != nil {
			return errors.New("无法确认 AP 出厂状态")
		}
		var state struct {
			Data struct {
				Code int `json:"code"`
			} `json:"data"`
		}
		page, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		probePath := filepath.Join(home, "private", "ap-reset-state.html")
		if readErr == nil {
			os.WriteFile(probePath, page, 0600)
			ownProbe(probePath)
		}
		e = json.Unmarshal(page, &state)
		if e == nil && state.Data.Code == -40407 {
			// The initial HTML route may only report an unauthenticated client.
			// Query the same unauthenticated data endpoint used by the firmware.
			payload := []byte(`{"method":"get","system":{"name":"sys"}}`)
			res, err := client.Post(base+"/", "application/json", bytes.NewReader(payload))
			if err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				if readErr == nil {
					os.WriteFile(probePath+".json", raw, 0600)
					ownProbe(probePath + ".json")
					e = json.Unmarshal(raw, &state)
				}
			}
		}
		if e != nil || (state.Data.Code != -40405 && state.Data.Code != -40407) {
			return fmt.Errorf("AP 出厂状态待核实（代码 %d），未创建或重置密码", state.Data.Code)
		}
		// ESYSCLIENTNORMAL describes an unauthenticated browser, not proof that
		// a password exists. The firmware's one-time set_password operation
		// determines whether initialization is allowed; no login guessing.
		if credentials.Password == "" {
			var random [12]byte
			if _, e = rand.Read(random[:]); e != nil {
				return e
			}
			credentials.Password = "Tr" + hex.EncodeToString(random[:]) + "7x"
		}
		if e = privateJSON(path, credentials); e != nil {
			return e
		}
		ownProbe(path)
		if _, e = routerJSON(client, base+"/", map[string]any{"method": "do", "set_password": map[string]string{"password": routerEncode(credentials.Password)}}); e != nil {
			return e
		}
	}
	login, e := routerJSON(client, base+"/", map[string]any{"method": "do", "login": map[string]string{"password": routerEncode(credentials.Password)}})
	if e != nil {
		return e
	}
	stok, ok := login["stok"].(string)
	if !ok || stok == "" {
		return errors.New("AP 未确认登录")
	}
	loginPath := filepath.Join(home, "private/ap-session.json")
	if e = privateJSON(loginPath, login); e != nil {
		return e
	}
	ownProbe(loginPath)
	// The firmware decodes the login stok before orgDsUrl encodes it again.
	stok, e = url.PathUnescape(stok)
	if e != nil {
		return errors.New("AP 会话格式无效")
	}
	if routerPassword != "" {
		if e = privateJSON(path, credentials); e != nil {
			return e
		}
		ownProbe(path)
	}
	endpoint := base + "/stok=" + routerSessionPath(stok) + "/ds"
	if _, e = routerJSON(client, endpoint, map[string]any{"method": "get", "system": map[string]any{"name": "sys"}}); e != nil {
		return fmt.Errorf("AP 已接受密码，但会话读取失败：%w", e)
	}
	if routerPrepareAP {
		before, e := routerJSON(client, endpoint, map[string]any{"method": "get", "network": map[string]any{"name": "lan"}, "dhcpd": map[string]any{"name": "udhcpd"}, "wireless": map[string]any{"name": []string{"wlan_host_2g", "wlan_host_5g", "wlan_bs"}}})
		if e != nil {
			return e
		}
		backup := filepath.Join(home, "private/ap-before-bridge.json")
		if e = privateJSON(backup, before); e != nil {
			return e
		}
		ownProbe(backup)
		// This exact operation comes from the verified mesh model's DHCP page:
		// explicit off changes every physical port to LAN and enables a LAN DHCP client.
		_, e = routerJSON(client, endpoint, map[string]any{"method": "set", "dhcpd": map[string]any{"udhcpd": map[string]any{"enable": 0, "auto": 0}}})
		if e != nil {
			return e
		}
		fmt.Println("TP-LINK accepted explicit DHCP off; all-LAN transition requires actual lease verification")
		return nil
	}
	info := map[string]any{}
	for _, item := range []struct {
		file     string
		sections []string
	}{
		{"system", []string{"sys"}}, {"device_info", []string{"info"}}, {"function", []string{"module_spec", "new_module_spec"}},
		{"network", []string{"lan"}}, {"protocol", []string{"wan"}}, {"dhcpd", []string{"udhcpd"}},
		{"wireless", []string{"wlan_host_2g", "wlan_host_5g", "wlan_bs"}},
	} {
		data, err := routerJSON(client, endpoint, map[string]any{"method": "get", item.file: map[string]any{"name": item.sections}})
		if err != nil {
			info[item.file] = map[string]string{"error": err.Error()}
		} else {
			info[item.file] = data
		}
	}
	infoPath := filepath.Join(home, "private", "ap-capabilities.json")
	if e = privateJSON(infoPath, info); e != nil {
		return e
	}
	ownProbe(infoPath)
	for _, page := range []string{"WlanNetwork2BandBS.htm", "WlanNetwork2Band.htm", "SysRebootNReset.htm", "SysBakNRestore.htm"} {
		res, err := client.Get(base + "/stok=" + routerSessionPath(stok) + "/pc/" + page)
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			continue
		}
		p := filepath.Join(home, "private", "router-assets", page)
		if e = os.WriteFile(p, content, 0600); e == nil {
			ownProbe(p)
		}
	}
	fmt.Println("AP authentication confirmed; capabilities saved privately")
	return nil
}

func routerSessionPath(stok string) string {
	// Unlike PathEscape, encodeURIComponent escapes '+' in a base64 session.
	return homeap.SessionPath(stok)
}
