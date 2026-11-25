package homegateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"treeroot/gopanel/homeap"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type AgentConfig struct {
	Server     string    `json:"server"`
	ID         string    `json:"id"`
	Token      string    `json:"token"`
	StateDir   string    `json:"state_dir"`
	Base       string    `json:"base"`
	LAN        string    `json:"lan"`
	Mode       string    `json:"mode"`
	AP         *APConfig `json:"ap,omitempty"`
	ControlDNS string    `json:"control_dns,omitempty"`
}
type AgentState struct {
	KernelBoot     string            `json:"kernel_boot"`
	Boot           string            `json:"boot"`
	Sequence       int64             `json:"sequence"`
	Applied        int64             `json:"applied"`
	Desired        Config            `json:"desired"`
	Results        []Result          `json:"results"`
	Completed      map[string]Result `json:"completed"`
	Events         []Event           `json:"events"`
	Devices        []Device          `json:"devices"`
	Error          string            `json:"error"`
	ControlError   string            `json:"control_error,omitempty"`
	FailedConfig   string            `json:"failed_config,omitempty"`
	FailedRevision int64             `json:"failed_revision,omitempty"`
	RetryAfter     int64             `json:"retry_after,omitempty"`
	Failures       int               `json:"failures,omitempty"`
	ChannelError   string            `json:"channel_error,omitempty"`
	LastSync       int64             `json:"last_sync,omitempty"`
	DroppedEvents  int64             `json:"dropped_events,omitempty"`
	WiFi           *homeap.WiFi      `json:"wifi,omitempty"`
}
type Agent struct {
	ConfigPath    string
	Config        AgentConfig
	State         AgentState
	client        *http.Client
	base          map[string]any
	secret        string
	api           string
	ledger        Ledger
	boundDevices  []Device
	exitAt        time.Time
	exitIP        string
	exitOK        int64
	droppedEvents atomic.Int64
	apChecked     time.Time
	started       time.Time
	sampledAt     int64
	sampledMono   int64
}

func Nonce() string { var b [24]byte; rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func Atomic(path string, data []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".home-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func ReadAgent(path string) (*Agent, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var c AgentConfig
	if json.Unmarshal(raw, &c) != nil {
		return nil, errors.New("网关配置无效")
	}
	if !strings.HasPrefix(c.Server, "https://") || c.ID == "" || len(c.Token) < 32 || !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Base) || c.LAN != "br-lan" || (c.Mode != "isolated" && c.Mode != "physical") {
		return nil, errors.New("网关连接或路径配置无效")
	}
	if c.AP != nil && (c.AP.Address != "192.168.77.3" || c.AP.MAC != "24:5a:5f:93:a5:71" || c.AP.Password == "") {
		return nil, errors.New("AP 配置与已核实硬件不符")
	}
	a := &Agent{ConfigPath: path, Config: c, started: time.Now(), client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.ControlDNS != "" {
		if _, _, e := net.SplitHostPort(c.ControlDNS); e != nil {
			return nil, errors.New("控制面 DNS 地址无效")
		}
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, c.ControlDNS)
		}}
		a.client.Transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second, Resolver: resolver}).DialContext, TLSHandshakeTimeout: 8 * time.Second}
	}
	raw, e = os.ReadFile(c.Base)
	if e != nil {
		return nil, e
	}
	if e = yaml.Unmarshal(raw, &a.base); e != nil {
		return nil, errors.New("现有 Mihomo 配置无法读取")
	}
	a.secret, _ = a.base["secret"].(string)
	if a.secret == "" {
		a.secret = Nonce()
	}
	a.api = "http://127.0.0.1:9090"
	if addr, ok := a.base["external-controller"].(string); ok && strings.HasPrefix(addr, "127.0.0.1:") {
		a.api = "http://" + addr
	}
	if raw, e = os.ReadFile(filepath.Join(c.StateDir, "state.json")); e == nil {
		if json.Unmarshal(raw, &a.State) != nil {
			return nil, errors.New("本地状态损坏，保留最后配置")
		}
	}
	a.State.Boot = Nonce()
	a.droppedEvents.Store(a.State.DroppedEvents)
	a.State.Sequence = 0
	if e = a.readLedger(); e != nil {
		return nil, errors.New("本地流量账本无法读取")
	}
	if a.State.Completed == nil {
		a.State.Completed = map[string]Result{}
	}
	kernelBoot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	for id, result := range a.State.Completed {
		if result.State != "running" {
			continue
		}
		if result.Kind == "gateway.reboot" && a.State.KernelBoot != "" && a.State.KernelBoot != string(kernelBoot) {
			result.State = "success"
			result.Detail = "家庭网关已重新启动"
		} else {
			result.State = "failed"
			result.Detail = "执行被中断，未重复执行"
		}
		a.State.Completed[id] = result
		a.State.Results = append(a.State.Results, result)
	}
	a.State.KernelBoot = string(kernelBoot)
	return a, nil
}
func (a *Agent) save() error {
	a.State.DroppedEvents = a.droppedEvents.Load()
	data, e := json.Marshal(a.State)
	if e != nil {
		return e
	}
	return Atomic(filepath.Join(a.Config.StateDir, "state.json"), data, 0600)
}

// Lock prevents two service instances from changing the same proxy/network.
func (a *Agent) Lock() (*os.File, error) {
	f, e := os.OpenFile(a.Config.Base+".home-agent.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("已有家庭管理程序运行，未启动第二个实例")
	}
	return f, nil
}
func (a *Agent) apiRequest(ctx context.Context, method, path string, body any, into any) error {
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, a.api+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+a.secret)
	r.Header.Set("Content-Type", "application/json")
	res, e := a.client.Do(r)
	if e != nil {
		return errors.New("Mihomo 控制接口未就绪")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("Mihomo 返回状态 %d", res.StatusCode)
	}
	if into != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(into)
	}
	return nil
}
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	data, e := c.Output()
	return data, e
}
func (a *Agent) apply(ctx context.Context, c Config, devices []Device) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if e := a.sample(ctx); e != nil {
		return e
	}
	rules, e := c.CompileRules(devices)
	if e != nil {
		return e
	}
	for i, line := range rules {
		if strings.HasSuffix(line, ",DIRECT,no-resolve") {
			rules[i] = strings.TrimSuffix(line, ",DIRECT,no-resolve") + "," + directAdapter + ",no-resolve"
		} else if strings.HasSuffix(line, ",DIRECT") {
			rules[i] = strings.TrimSuffix(line, ",DIRECT") + "," + directAdapter
		}
	}
	raw, _ := json.Marshal(a.base)
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	cfg["rules"] = rules
	cfg["mode"] = "rule"
	cfg["log-level"] = "info"
	cfg["allow-lan"] = true
	cfg["bind-address"] = "*"
	cfg["mixed-port"] = 0
	cfg["socks-port"] = 0
	cfg["port"] = 0
	cfg["redir-port"] = 0
	cfg["tproxy-port"] = 7895
	cfg["ipv6"] = false
	cfg["tun"] = map[string]any{"enable": false}
	cfg["external-controller"] = "127.0.0.1:9090"
	cfg["external-controller-tls"] = ""
	cfg["external-controller-unix"] = ""
	healthProxy := map[string]string{"direct": "DIRECT", "reject": "REJECT", "proxy": "HC-" + c.DefaultNode}[c.Default]
	cfg["listeners"] = []any{map[string]any{"name": "home-health", "type": "http", "listen": "127.0.0.1", "port": 17895, "proxy": healthProxy}}
	if a.secret == "" {
		a.secret = Nonce()
	}
	cfg["secret"] = a.secret
	cfg["sniffer"] = map[string]any{"enable": true, "force-dns-mapping": true, "parse-pure-ip": true, "sniff": map[string]any{"HTTP": map[string]any{"ports": []string{"80", "8080-8880"}}, "TLS": map[string]any{"ports": []int{443, 8443}}, "QUIC": map[string]any{"ports": []int{443, 8443}}}}
	cfg["profile"] = map[string]any{"store-fake-ip": true}
	proxies := []map[string]any{{"name": directAdapter, "type": "direct", "routing-mark": directMark}}
	for _, n := range c.Nodes {
		if n.Proxy == nil {
			return errors.New("代理节点缺少配置")
		}
		p := map[string]any{}
		for k, v := range n.Proxy {
			p[k] = v
		}
		p["name"] = "HG-" + n.ID
		p["routing-mark"] = proxyMark
		proxies = append(proxies, p)
		health := map[string]any{}
		for k, v := range p {
			health[k] = v
		}
		health["name"] = "HC-" + n.ID
		health["routing-mark"] = 0
		proxies = append(proxies, health)
	}
	cfg["proxies"] = proxies
	cfg["proxy-groups"] = []any{}
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		dns = map[string]any{}
	}
	dns["enable"] = true
	dns["listen"] = "127.0.0.1:1053"
	dns["enhanced-mode"] = "fake-ip"
	dns["fake-ip-range"] = "198.18.0.1/16"
	dns["ipv6"] = false
	if dns["nameserver"] == nil {
		dns["nameserver"] = []string{"223.5.5.5", "119.29.29.29"}
	}
	cfg["dns"] = dns
	candidate := filepath.Join(a.Config.StateDir, "candidate.json")
	data, e := json.Marshal(cfg)
	if e != nil {
		return e
	}
	if e = Atomic(candidate, data, 0600); e != nil {
		return e
	}
	if _, e = run(ctx, "/usr/bin/mihomo", "-t", "-f", candidate, "-d", "/etc/mihomo"); e != nil {
		return errors.New("Mihomo 配置校验失败，未应用")
	}
	transaction, e := a.beginRecovery(ctx)
	if e != nil {
		return e
	}
	committed := false
	defer func() {
		if !committed {
			recoveryCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			a.restore(recoveryCtx, transaction)
		}
	}()
	if e = Atomic(a.Config.Base, data, 0600); e != nil {
		return e
	}
	if e = a.restartProxy(ctx); e != nil {
		return errors.New("代理重启失败，已恢复")
	}
	a.api = "http://127.0.0.1:9090"
	if e = a.waitConfiguration(ctx, c); e != nil {
		return e
	}
	previous, _ := json.Marshal(a.State.Desired)
	next, _ := json.Marshal(c)
	// Reboot recovery and DHCP changes must work during a control/WAN outage.
	// Only a new policy or changed node credentials need fresh Internet checks.
	if a.State.Applied == 0 || !bytes.Equal(previous, next) {
		if e = a.verifyConnectivity(ctx, c); e != nil {
			return e
		}
	}
	if e = a.applyDHCP(ctx, c); e != nil {
		return e
	}
	if e = a.applyFirewall(ctx, c, devices); e != nil {
		return e
	}
	a.State.Desired = c
	a.State.Applied = c.Revision
	a.State.Error = ""
	a.State.FailedConfig, a.State.RetryAfter, a.State.Failures = "", 0, 0
	a.State.FailedRevision = 0
	a.exitAt, a.exitIP, a.exitOK = time.Time{}, "", 0
	a.boundDevices = append([]Device(nil), devices...)
	if e = a.save(); e != nil {
		return e
	}
	if e = os.Remove(a.recoveryPath()); e != nil {
		return e
	}
	committed = true
	return nil
}
func (a *Agent) applyDHCP(ctx context.Context, c Config) error {
	existing, e := run(ctx, "uci", "show", "dhcp")
	if e != nil {
		return errors.New("无法读取 DHCP")
	}
	for _, line := range strings.Split(string(existing), "\n") {
		name, _, ok := strings.Cut(line, "=")
		if ok && strings.HasPrefix(name, "dhcp.hg_") && strings.Count(name, ".") == 1 {
			if _, e = run(ctx, "uci", "-q", "delete", name); e != nil {
				return e
			}
		}
	}
	for _, d := range c.Devices {
		if d.FixedIP == "" {
			continue
		}
		section := "dhcp.hg_" + strings.ReplaceAll(d.MAC, ":", "")
		for _, kv := range []string{section + "=host", section + ".mac=" + d.MAC, section + ".ip=" + d.FixedIP} {
			if _, e = run(ctx, "uci", "set", kv); e != nil {
				return errors.New("固定地址应用失败")
			}
		}
	}
	if _, e = run(ctx, "uci", "commit", "dhcp"); e != nil {
		return e
	}
	_, e = run(ctx, "/etc/init.d/dnsmasq", "restart")
	return e
}

func (a *Agent) Diagnostics(ctx context.Context) map[string]any {
	return a.health(ctx)
}
