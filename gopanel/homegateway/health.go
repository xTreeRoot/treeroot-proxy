package homegateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
	"treeroot/gopanel/homeap"
)

func (a *Agent) waitConfiguration(ctx context.Context, c Config) error {
	for range 50 {
		var inventory struct {
			Proxies map[string]json.RawMessage `json:"proxies"`
		}
		ready := a.apiRequest(ctx, "GET", "/proxies", nil, &inventory) == nil
		for _, node := range c.Nodes {
			if _, ok := inventory.Proxies["HG-"+node.ID]; !ok {
				ready = false
			}
		}
		if ready {
			conn, e := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", "127.0.0.1:17895")
			if e == nil {
				conn.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("家庭代理配置尚未就绪")
}

func (a *Agent) health(ctx context.Context) map[string]any {
	info := map[string]any{"mode": a.Config.Mode, "lan": a.Config.LAN, "version": Version, "ap_control": "unverified", "ipv6": "blocked_on_lan"}
	if a.Config.AP != nil {
		if time.Since(a.apChecked) > 30*time.Second {
			if _, e := a.APStatus(ctx); e != nil {
				a.apChecked = time.Now()
				a.State.WiFi = &homeap.WiFi{Online: false}
			}
		}
		info["wifi"] = a.State.WiFi
		info["ap_control"] = "tplink_verified"
	}
	var version map[string]any
	info["proxy_ready"] = a.apiRequest(ctx, "GET", "/version", nil, &version) == nil
	if version != nil {
		info["proxy_version"] = version["version"]
	}
	var uptime float64
	data, _ := os.ReadFile("/proc/uptime")
	fmt.Sscanf(string(data), "%f", &uptime)
	info["uptime"] = int64(uptime)
	data, _ = os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			info["memory_available"] = strings.TrimSpace(strings.TrimPrefix(line, "MemAvailable:"))
		}
	}
	info["applied"] = a.State.Applied
	info["channel_error"] = a.State.ChannelError
	info["last_sync"] = a.State.LastSync
	info["events_pending"] = len(a.State.Events)
	info["events_dropped"] = a.droppedEvents.Load()
	if time.Since(a.exitAt) > time.Minute {
		a.exitAt = time.Now()
		proxy, _ := url.Parse("http://127.0.0.1:17895")
		transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		client := &http.Client{Timeout: 8 * time.Second, Transport: transport}
		request, _ := http.NewRequestWithContext(ctx, "GET", "https://www.cloudflare.com/cdn-cgi/trace", nil)
		if response, e := client.Do(request); e == nil {
			scanner := bufio.NewScanner(io.LimitReader(response.Body, 4096))
			for scanner.Scan() {
				if value, ok := strings.CutPrefix(scanner.Text(), "ip="); ok {
					if ip, e := netip.ParseAddr(value); e == nil {
						a.exitIP = ip.String()
						a.exitOK = time.Now().Unix()
					}
				}
			}
			response.Body.Close()
		}
		transport.CloseIdleConnections()
	}
	info["default_policy_exit"] = a.exitIP
	info["exit_checked"] = a.exitOK
	return info
}

func (a *Agent) verifyConnectivity(ctx context.Context, c Config) error {
	for _, node := range c.Nodes {
		var result struct {
			Delay int64 `json:"delay"`
		}
		var last error
		for _, target := range []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://www.baidu.com/"} {
			path := "/proxies/" + url.PathEscape("HG-"+node.ID) + "/delay?timeout=6000&url=" + url.QueryEscape(target)
			last = a.apiRequest(ctx, "GET", path, nil, &result)
			if last == nil {
				break
			}
		}
		if last != nil {
			return fmt.Errorf("代理节点连通性检查失败：%w", last)
		}
	}
	if c.Default == "reject" {
		return nil
	}
	proxy, _ := url.Parse("http://127.0.0.1:17895")
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	targets := []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://www.baidu.com/"}
	for _, target := range targets {
		request, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		res, e := client.Do(request)
		if e == nil {
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 500 && res.StatusCode != 407 {
				return nil
			}
		}
	}
	return fmt.Errorf("默认策略未通过实际连通性检查")
}
