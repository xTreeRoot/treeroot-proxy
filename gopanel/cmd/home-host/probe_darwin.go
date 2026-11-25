package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type cableResult struct {
	Checked        int64  `json:"checked"`
	Link           bool   `json:"link"`
	DHCP           bool   `json:"dhcp"`
	Server         string `json:"server,omitempty"`
	Offered        string `json:"offered,omitempty"`
	Router         string `json:"router,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	HTTPServer     string `json:"http_server,omitempty"`
	PageTitle      string `json:"page_title,omitempty"`
	InterfaceState string `json:"interface_state,omitempty"`
	ServiceState   string `json:"service_state,omitempty"`
	QueryRoute     string `json:"query_route,omitempty"`
	QueryInterface string `json:"query_interface,omitempty"`
	Neighbors      string `json:"neighbors,omitempty"`
	RouterMAC      string `json:"router_mac,omitempty"`
	Error          string `json:"error,omitempty"`
}

func boundEthernet(_ string, _ string, c syscall.RawConn) error {
	var e error
	if ce := c.Control(func(fd uintptr) {
		iface, err := net.InterfaceByName("en0")
		if err != nil {
			e = err
			return
		}
		e = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
		if e == nil {
			e = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
		}
	}); ce != nil {
		return ce
	}
	return e
}
func dhcpOptions(data []byte) (map[byte][]byte, error) {
	if len(data) < 240 || binary.BigEndian.Uint32(data[236:240]) != 0x63825363 {
		return nil, errors.New("无效 DHCP 应答")
	}
	out := map[byte][]byte{}
	for i := 240; i < len(data); {
		typ := data[i]
		i++
		if typ == 255 {
			break
		}
		if typ == 0 {
			continue
		}
		if i >= len(data) {
			break
		}
		size := int(data[i])
		i++
		if i+size > len(data) {
			break
		}
		out[typ] = append([]byte(nil), data[i:i+size]...)
		i += size
	}
	return out, nil
}
func probeCable() (probeErr error) {
	if e := prepareNetwork(); e != nil {
		return e
	}
	result := cableResult{Checked: time.Now().Unix()}
	defer func() {
		path := filepath.Join(home, "private", "cable-probe.json")
		privateJSON(path, result)
		ownProbe(path)
	}()
	defer func() {
		if e := rollbackNetwork(); e != nil {
			result.Error = "网口恢复失败，请保留回退记录"
			probeErr = e
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for range 45 {
		raw, _ := hostOutput(ctx, "/sbin/ifconfig", "en0")
		result.InterfaceState = raw
		result.ServiceState, _ = hostOutput(ctx, "/usr/sbin/networksetup", "-getnetworkserviceenabled", "Ethernet")
		if strings.Contains(raw, "status: active") {
			result.Link = true
			break
		}
		time.Sleep(time.Second)
	}
	if !result.Link {
		result.Error = "未检测到网线连接"
		fmt.Println("No Ethernet link; original network is being restored")
		return nil
	}
	if _, e := hostOutput(ctx, "/sbin/route", "-n", "add", "-host", "-ifscope", "en0", "255.255.255.255", "-interface", "en0"); e != nil {
		return errors.New("无法创建仅用于 Ethernet 的广播路径")
	}
	defer hostOutput(context.Background(), "/sbin/route", "-n", "delete", "-host", "-ifscope", "en0", "255.255.255.255")
	iface, e := net.InterfaceByName("en0")
	if e != nil {
		return e
	}
	listener := net.ListenConfig{Control: boundEthernet}
	conn, e := listener.ListenPacket(ctx, "udp4", "0.0.0.0:68")
	if e != nil {
		return errors.New("无法创建仅绑定 Ethernet 的 DHCP 探测")
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	packet := make([]byte, 240)
	packet[0] = 1
	packet[1] = 1
	packet[2] = 6
	rand.Read(packet[4:8])
	binary.BigEndian.PutUint16(packet[10:12], 0x8000)
	copy(packet[28:34], iface.HardwareAddr)
	binary.BigEndian.PutUint32(packet[236:240], 0x63825363)
	discover := append(append([]byte(nil), packet...), 53, 1, 1, 55, 4, 1, 3, 6, 54, 255)
	if _, e = conn.WriteTo(discover, &net.UDPAddr{IP: net.IPv4bcast, Port: 67}); e != nil {
		return e
	}
	buffer := make([]byte, 4096)
	var offer []byte
	var options map[byte][]byte
	for {
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			result.Error = "未收到 DHCP 应答；不能据此判断 WAN/LAN 模式"
			fmt.Println("DHCP probe had no offer; original network is being restored")
			return nil
		}
		if n < 240 || buffer[0] != 2 || string(buffer[4:8]) != string(packet[4:8]) || string(buffer[28:34]) != string(iface.HardwareAddr) {
			continue
		}
		options, e = dhcpOptions(buffer[:n])
		if e == nil && len(options[53]) == 1 && options[53][0] == 2 {
			offer = append([]byte(nil), buffer[:n]...)
			break
		}
	}
	if len(options[54]) != 4 {
		return errors.New("DHCP 应答缺少服务器地址")
	}
	result.DHCP = true
	result.Server = net.IP(options[54]).String()
	result.Offered = net.IP(offer[16:20]).String()
	serverIP, serverErr := netip.ParseAddr(result.Server)
	offerIP, offerErr := netip.ParseAddr(result.Offered)
	if serverErr != nil || offerErr != nil || !serverIP.IsPrivate() || !offerIP.IsPrivate() || serverIP == offerIP || result.Offered == "192.168.77.2" {
		return errors.New("DHCP 地址不符合隔离探测要求，未应用租约")
	}
	if len(options[3]) >= 4 {
		result.Router = net.IP(options[3][:4]).String()
	}
	// Request the offered address. Only a subnet disjoint from Wi-Fi may be installed;
	// no DHCP routes, DNS settings or default gateway are accepted.
	request := append(append([]byte(nil), packet...), 53, 1, 3, 50, 4)
	request = append(request, offer[16:20]...)
	request = append(request, 54, 4)
	request = append(request, options[54]...)
	request = append(request, 255)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.WriteTo(request, &net.UDPAddr{IP: net.IPv4bcast, Port: 67})
	acked := false
	for {
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			break
		}
		if n < 240 || string(buffer[4:8]) != string(packet[4:8]) {
			continue
		}
		o, err := dhcpOptions(buffer[:n])
		if err == nil && len(o[53]) == 1 && o[53][0] == 5 {
			acked = true
			break
		}
	}
	if !acked {
		result.Error = "未确认 DHCP 租约，未使用该地址"
		return nil
	}
	// Never use an alias equal to the current Wi-Fi address.
	wifi, _ := net.InterfaceByName("en1")
	if wifi != nil {
		addrs, _ := wifi.Addrs()
		for _, addr := range addrs {
			if strings.HasPrefix(addr.String(), result.Offered+"/") {
				return errors.New("租约与 Wi-Fi 地址冲突，拒绝使用")
			}
		}
	}
	mask := "255.255.255.255"
	if rawMask := options[1]; len(rawMask) == 4 {
		ones, bits := net.IPMask(rawMask).Size()
		if bits == 32 && ones >= 16 && ones <= 30 {
			candidate, _ := netip.ParseAddr(result.Offered)
			proposed := netip.PrefixFrom(candidate, ones).Masked()
			overlap := false
			if wifi != nil {
				addrs, _ := wifi.Addrs()
				for _, addr := range addrs {
					if prefix, e := netip.ParsePrefix(addr.String()); e == nil && prefix.Addr().Is4() && (prefix.Contains(candidate) || proposed.Contains(prefix.Addr())) {
						overlap = true
					}
				}
			}
			if !overlap {
				mask = net.IP(rawMask).String()
			}
		}
	}
	if mask == "255.255.255.255" {
		result.Error = "路由器网段与 Wi-Fi 重叠，已保留 DHCP 探测结果，未创建冲突路由"
		return nil
	}
	// The independent recovery process must also know about the probe alias.
	rawSnapshot, e := os.ReadFile(networkPending())
	if e != nil {
		return e
	}
	var snapshot networkSnapshot
	if json.Unmarshal(rawSnapshot, &snapshot) != nil {
		return errors.New("网络回退记录无效")
	}
	snapshot.ProbeIP = result.Offered
	if e = privateJSON(networkPending(), snapshot); e != nil {
		return e
	}
	if _, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "inet", result.Offered, "netmask", mask, "alias"); e != nil {
		return e
	}
	defer hostOutput(context.Background(), "/sbin/ifconfig", "en0", "inet", result.Offered, "-alias")
	if route, e := hostOutput(ctx, "/sbin/route", "-n", "get", "default"); e != nil || !strings.Contains(route, "interface: en1") {
		return errors.New("默认路由发生变化，已请求回退")
	}
	result.QueryRoute, _ = hostOutput(ctx, "/sbin/route", "-n", "get", "-ifscope", "en0", result.Server)
	result.QueryInterface, _ = hostOutput(ctx, "/sbin/ifconfig", "en0")
	dialer := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(result.Offered)}, Control: boundEthernet}
	transport := &http.Transport{DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Get("http://" + result.Server + "/")
	if e != nil {
		result.Error = "路由器 HTTP 接口未响应：" + e.Error()
		result.Neighbors, _ = hostOutput(ctx, "/usr/sbin/arp", "-an", "-i", "en0")
		return nil
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	result.Neighbors, _ = hostOutput(ctx, "/usr/sbin/arp", "-an", "-i", "en0")
	for _, line := range strings.Split(result.Neighbors, "\n") {
		if strings.Contains(line, "("+result.Server+")") {
			if match := regexp.MustCompile(` at ([0-9a-f:]+) on en0`).FindStringSubmatch(line); len(match) > 1 {
				if mac, e := net.ParseMAC(match[1]); e == nil {
					result.RouterMAC = mac.String()
				}
			}
		}
	}
	result.HTTPServer = response.Header.Get("Server")
	html, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if e != nil {
		return e
	}
	pagePath := filepath.Join(home, "private", "router-probe.html")
	if e = os.WriteFile(pagePath, html, 0600); e != nil {
		return e
	}
	ownProbe(pagePath)
	if (routerInitialize || routerInspect || routerStateOnly) && result.RouterMAC != "24:5a:5f:93:a5:71" {
		return errors.New("回应设备的 MAC 与已核实 TP-LINK 不符，未发送认证或设置请求")
	}
	if routerStateOnly {
		return checkRouterState(client, "http://"+result.Server)
	}
	if routerInitialize || routerInspect {
		return inspectRouter(client, "http://"+result.Server, routerInitialize)
	}
	title := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindSubmatch(html)
	if len(title) > 1 {
		result.PageTitle = strings.TrimSpace(string(title[1]))
	}
	assetDir := filepath.Join(home, "private", "router-assets")
	if e = os.MkdirAll(assetDir, 0700); e != nil {
		return e
	}
	ownProbe(assetDir)
	base, _ := url.Parse("http://" + result.Server + "/")
	manifest := map[string]string{}
	var queue []string
	for _, match := range regexp.MustCompile(`(?is)<script[^>]+src=["']([^"']+)["']`).FindAllSubmatch(html, 8) {
		queue = append(queue, string(match[1]))
	}
	seen := map[string]bool{}
	dependencies := regexp.MustCompile(`url:\s*["']([^"']+\.js)["']`)
	for len(queue) > 0 && len(seen) < 24 {
		ref := queue[0]
		queue = queue[1:]
		reference, err := url.Parse(ref)
		if err != nil {
			continue
		}
		target := base.ResolveReference(reference)
		allowedPage := target.Path == "/pc/BridgeSwitch.htm" || target.Path == "/pc/DHCPServer.htm" || target.Path == "/pc/Login.htm"
		if target.Host != base.Host || target.Scheme != "http" || (!strings.HasPrefix(target.Path, "/web-static/") && !allowedPage) || seen[target.Path] {
			continue
		}
		seen[target.Path] = true
		response, err := client.Get(target.String())
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			continue
		}
		digest := sha256.Sum256([]byte(target.Path))
		name := fmt.Sprintf("%x.js", digest[:8])
		if allowedPage {
			name = fmt.Sprintf("%x.html", digest[:8])
		}
		path := filepath.Join(assetDir, name)
		if os.WriteFile(path, content, 0600) == nil {
			ownProbe(path)
			manifest[target.Path] = name
			for _, dependency := range dependencies.FindAllSubmatch(content, 24) {
				queue = append(queue, string(dependency[1]))
			}
			// These page names and the ../pc/ prefix are defined by the fetched
			// router's own menu and PageFunc, rather than another model's API.
			for _, page := range []string{"BridgeSwitch.htm", "DHCPServer.htm", "Login.htm"} {
				if strings.Contains(string(content), `"`+page+`"`) {
					queue = append(queue, "/pc/"+page)
				}
			}
		}
	}
	path := filepath.Join(assetDir, "manifest.json")
	privateJSON(path, manifest)
	ownProbe(path)
	fmt.Println("Cable capability probe saved privately; original network is being restored")
	return nil
}

func ownProbe(path string) {
	if stat, e := os.Stat(filepath.Join(home, "private")); e == nil {
		if owner, ok := stat.Sys().(*syscall.Stat_t); ok {
			os.Chown(path, int(owner.Uid), int(owner.Gid))
		}
	}
}
