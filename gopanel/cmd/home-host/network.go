package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type networkSnapshot struct {
	ID              string `json:"id"`
	Created         int64  `json:"created"`
	Deadline        int64  `json:"deadline"`
	Ethernet        string `json:"ethernet"`
	EthernetEnabled string `json:"ethernet_enabled"`
	EthernetDNS     string `json:"ethernet_dns"`
	EthernetSearch  string `json:"ethernet_search"`
	WiFi            string `json:"wifi"`
	DefaultRoute    string `json:"default_route"`
	Interfaces      string `json:"interfaces"`
	ProbeIP         string `json:"probe_ip,omitempty"`
	ProbeVMPID      int    `json:"probe_vm_pid,omitempty"`
	ProbeVMDir      string `json:"probe_vm_dir,omitempty"`
}

func hostOutput(ctx context.Context, name string, args ...string) (string, error) {
	raw, e := exec.CommandContext(ctx, name, args...).Output()
	return string(raw), e
}
func privateJSON(path string, value any) error {
	data, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".network-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	tmp.Chmod(0600)
	_, e = tmp.Write(data)
	if e == nil {
		e = tmp.Sync()
	}
	if ce := tmp.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp.Name(), path)
}
func networkSnapshotNow() (networkSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var s networkSnapshot
	s.Created = time.Now().Unix()
	s.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	queries := []struct {
		dest *string
		name string
		args []string
	}{
		{&s.Ethernet, "/usr/sbin/networksetup", []string{"-getinfo", "Ethernet"}},
		{&s.EthernetEnabled, "/usr/sbin/networksetup", []string{"-getnetworkserviceenabled", "Ethernet"}},
		{&s.EthernetDNS, "/usr/sbin/networksetup", []string{"-getdnsservers", "Ethernet"}},
		{&s.EthernetSearch, "/usr/sbin/networksetup", []string{"-getsearchdomains", "Ethernet"}},
		{&s.WiFi, "/usr/sbin/networksetup", []string{"-getinfo", "Wi-Fi"}},
		{&s.DefaultRoute, "/sbin/route", []string{"-n", "get", "default"}},
		{&s.Interfaces, "/sbin/ifconfig", []string{"en0"}},
	}
	for _, q := range queries {
		v, e := hostOutput(ctx, q.name, q.args...)
		if e != nil {
			return s, errors.New("无法读取当前网络，未修改任何设置")
		}
		*q.dest = v
	}
	return s, nil
}
func snapshotNetwork() error {
	s, e := networkSnapshotNow()
	if e != nil {
		return e
	}
	path := filepath.Join(home, "private", "mac-network-"+s.ID+".json")
	if e = privateJSON(path, s); e != nil {
		return e
	}
	fmt.Println("Network snapshot saved privately; no network settings changed")
	return nil
}
func networkPending() string { return filepath.Join(home, "private", "mac-network-pending.json") }
func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("请在 Codex 终端中使用本机管理员授权")
	}
	return nil
}

func fixedEthernetBaseline(s networkSnapshot) bool {
	return strings.TrimSpace(s.EthernetEnabled) == "Enabled" && strings.Contains(s.Ethernet, "Manual Configuration") &&
		strings.Contains(s.Ethernet, "IP address: 192.168.77.2\n") && strings.Contains(s.Ethernet, "Subnet mask: 255.255.255.0\n") &&
		strings.Contains(s.Ethernet, "Router: 0.0.0.0\n") && strings.Contains(s.Ethernet, "IPv6: Off")
}

func disabledEthernetBaseline(s networkSnapshot) bool {
	return strings.TrimSpace(s.EthernetEnabled) == "Disabled" && strings.Contains(s.Ethernet, "DHCP Configuration") && strings.Contains(s.Ethernet, "IPv6: Automatic")
}
func prepareNetwork() error {
	if e := requireRoot(); e != nil {
		return e
	}
	if _, e := os.Stat(networkPending()); e == nil {
		return errors.New("有尚未确认的网络调整，请先回退")
	}
	s, e := networkSnapshotNow()
	if e != nil {
		return e
	}
	// Only the verified disabled DHCP baseline is supported. Other states need a new plan.
	if (!disabledEthernetBaseline(s) && !fixedEthernetBaseline(s)) || !strings.Contains(s.DefaultRoute, "interface: en1") {
		return errors.New("网络基线与已核实方案不符，未启用网口")
	}
	s.Deadline = time.Now().Add(3 * time.Minute).Unix()
	if e = privateJSON(networkPending(), s); e != nil {
		return e
	}
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	log, e := os.OpenFile(filepath.Join(home, "logs", "network-recovery.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	watchdog := exec.Command(binary, "-home", home, "watch-network")
	watchdog.Stdout = log
	watchdog.Stderr = log
	watchdog.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = watchdog.Start(); e != nil {
		os.Remove(networkPending())
		return e
	}
	go watchdog.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, args := range [][]string{{"-setmanual", "Ethernet", "192.168.77.2", "255.255.255.0", "0.0.0.0"}, {"-setv6off", "Ethernet"}, {"-setnetworkserviceenabled", "Ethernet", "on"}} {
		if _, e = hostOutput(ctx, "/usr/sbin/networksetup", args...); e != nil {
			rollbackNetwork()
			return errors.New("网口准备失败，已请求回退")
		}
	}
	if _, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "up"); e != nil {
		rollbackNetwork()
		return errors.New("网卡启动失败，已请求回退")
	}
	if _, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "inet", "192.168.77.2", "netmask", "255.255.255.0"); e != nil {
		rollbackNetwork()
		return errors.New("网口静态地址应用失败，已请求回退")
	}
	route, e := hostOutput(ctx, "/sbin/route", "-n", "get", "default")
	if e != nil || !strings.Contains(route, "interface: en1") {
		rollbackNetwork()
		return errors.New("默认路由发生变化，已请求回退")
	}
	fmt.Println("Ethernet uses a fixed LAN address with no default gateway; Wi-Fi default route preserved. Automatic rollback in 3 minutes unless confirmed.")
	return nil
}
func rollbackNetwork() error {
	if e := requireRoot(); e != nil {
		return e
	}
	raw, e := os.ReadFile(networkPending())
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var s networkSnapshot
	if json.Unmarshal(raw, &s) != nil || (!disabledEthernetBaseline(s) && !fixedEthernetBaseline(s)) {
		return errors.New("网络快照无效，保留恢复记录")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.ProbeVMPID > 1 && s.ProbeVMDir != "" {
		runtime, err := activeRuntime(home)
		if err != nil {
			return err
		}
		expectedPrefix := filepath.Join(runtime, "private", "ap-probe-")
		args, err := hostOutput(ctx, "/bin/ps", "-p", fmt.Sprint(s.ProbeVMPID), "-o", "command=")
		if err == nil && strings.HasPrefix(s.ProbeVMDir, expectedPrefix) && strings.Contains(args, "-name home-router-probe ") && strings.Contains(args, s.ProbeVMDir) {
			syscall.Kill(s.ProbeVMPID, syscall.SIGTERM)
		}
	}
	if s.ProbeIP != "" {
		hostOutput(ctx, "/sbin/ifconfig", "en0", "inet", s.ProbeIP, "-alias")
	}
	if fixedEthernetBaseline(s) {
		for _, args := range [][]string{{"-setmanual", "Ethernet", "192.168.77.2", "255.255.255.0", "0.0.0.0"}, {"-setv6off", "Ethernet"}, {"-setnetworkserviceenabled", "Ethernet", "on"}} {
			if _, e = hostOutput(ctx, "/usr/sbin/networksetup", args...); e != nil {
				return errors.New("固定有线配置恢复失败，保留恢复记录")
			}
		}
		if _, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "up"); e != nil {
			return e
		}
		if e = os.Remove(networkPending()); e != nil {
			return e
		}
		fmt.Println("Fixed Ethernet LAN remains enabled; Wi-Fi default route preserved")
		return nil
	}
	for _, args := range [][]string{{"-setnetworkserviceenabled", "Ethernet", "off"}, {"-setdhcp", "Ethernet"}, {"-setv6automatic", "Ethernet"}} {
		if _, e = hostOutput(ctx, "/usr/sbin/networksetup", args...); e != nil {
			return errors.New("网口恢复失败，保留恢复记录")
		}
	}
	if e := clearProbeInterface(ctx); e != nil {
		return e
	}
	if e = os.Remove(networkPending()); e != nil {
		return e
	}
	fmt.Println("Ethernet disabled and DHCP/IPv6 baseline restored; Wi-Fi untouched")
	return nil
}

func clearProbeInterface(ctx context.Context) error {
	enabled, e := hostOutput(ctx, "/usr/sbin/networksetup", "-getnetworkserviceenabled", "Ethernet")
	if e != nil || strings.TrimSpace(enabled) != "Disabled" {
		return errors.New("网口未停用，拒绝清理地址")
	}
	raw, e := hostOutput(ctx, "/sbin/ifconfig", "en0")
	if e != nil {
		return e
	}
	if strings.Contains(raw, "inet 192.168.77.2 ") {
		if _, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "inet", "192.168.77.2", "-alias"); e != nil {
			return e
		}
	}
	_, e = hostOutput(ctx, "/sbin/ifconfig", "en0", "down")
	return e
}
func cleanupNetwork() error {
	if e := requireRoot(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return clearProbeInterface(ctx)
}
func watchNetwork() error {
	raw, e := os.ReadFile(networkPending())
	if e != nil {
		return e
	}
	var s networkSnapshot
	if json.Unmarshal(raw, &s) != nil {
		return errors.New("invalid network snapshot")
	}
	timer := time.NewTimer(max(0, time.Until(time.Unix(s.Deadline, 0))))
	defer timer.Stop()
	<-timer.C
	raw, e = os.ReadFile(networkPending())
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var latest networkSnapshot
	if json.Unmarshal(raw, &latest) != nil || latest.ID != s.ID {
		return nil
	}
	return rollbackNetwork()
}
func confirmNetwork() error {
	if e := requireRoot(); e != nil {
		return e
	}
	raw, e := os.ReadFile(networkPending())
	if e != nil {
		return e
	}
	var s networkSnapshot
	if json.Unmarshal(raw, &s) != nil || time.Now().Unix() >= s.Deadline {
		return errors.New("网络确认已过期")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	route, e := hostOutput(ctx, "/sbin/route", "-n", "get", "default")
	if e != nil || !strings.Contains(route, "interface: en1") {
		return errors.New("Wi-Fi 默认路由未保持，拒绝确认")
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", "https://proxy.treeroot.top/api/me", nil)
	client := &http.Client{Timeout: 8 * time.Second}
	res, e := client.Do(request)
	if e != nil {
		return errors.New("现有上网连接验证失败，等待自动回退")
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("后台 HTTPS 验证失败")
	}
	if e = os.Remove(networkPending()); e != nil {
		return e
	}
	fmt.Println("Wi-Fi connectivity verified; fixed Ethernet LAN configuration confirmed")
	return nil
}
