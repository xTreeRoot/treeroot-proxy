package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A disposable guest avoids installing an overlapping subnet on the Mac. Its
// physical NIC is hotplugged only after DHCP servers and bridges are stopped.
func inspectRouterVM() (result error) {
	if err := requireRoot(); err != nil {
		return err
	}
	runtime, err := activeRuntime(home)
	if err != nil {
		return err
	}
	folder, err := os.MkdirTemp(filepath.Join(runtime, "private"), "ap-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(folder)
	disk := filepath.Join(folder, "probe.qcow2")
	base := filepath.Join(home, "private", "go-lab-client.qcow2")
	if data, err := exec.Command("/opt/homebrew/bin/qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", base, disk).CombinedOutput(); err != nil {
		return fmt.Errorf("无法创建临时探测磁盘：%s", strings.TrimSpace(string(data)))
	}
	socket := filepath.Join(folder, "serial.sock")
	monitor := filepath.Join(folder, "qmp.sock")
	for _, p := range []string{socket, monitor} {
		os.Remove(p)
		defer os.Remove(p)
	}
	args := []string{"-name", "home-router-probe", "-machine", "virt,accel=hvf", "-cpu", "host", "-smp", "1", "-m", "256", "-bios", "/opt/homebrew/share/qemu/edk2-aarch64-code.fd", "-drive", "if=virtio,format=qcow2,file=" + disk,
		"-netdev", "user,id=management,ipv6=off,hostfwd=tcp:127.0.0.1:22078-:22", "-device", "virtio-net-pci,netdev=management,mac=52:54:00:77:02:01",
		"-netdev", "hubport,id=apnic,hubid=78", "-device", "virtio-net-pci,netdev=apnic,mac=52:54:00:77:02:02",
		"-display", "none", "-monitor", "none", "-serial", "unix:" + socket + ",server=on,wait=off", "-qmp", "unix:" + monitor + ",server=on,wait=off"}
	log, err := os.OpenFile(filepath.Join(home, "logs", "ap-probe-vm.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	vm := exec.Command("/opt/homebrew/bin/qemu-system-aarch64", args...)
	vm.Stdout, vm.Stderr = log, log
	vm.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = vm.Start(); err != nil {
		return err
	}
	defer func() { vm.Process.Kill(); vm.Wait() }()
	socketReady := false
	for i := 0; i < 150; i++ {
		if _, err = os.Stat(socket); err == nil {
			socketReady = true
			break
		}
		if vm.Process.Signal(syscall.Signal(0)) != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !socketReady {
		vm.Process.Kill()
		vm.Wait()
		if data, e := os.ReadFile(filepath.Join(home, "logs", "ap-probe-vm.log")); e == nil && len(data) > 0 {
			return fmt.Errorf("临时隔离探测机未启动：%s", strings.TrimSpace(string(data)))
		}
		return errors.New("临时隔离探测机未启动，网线未接入")
	}
	// Let init finish before transmitting console input. The virtual UART can
	// drop input while early boot writes continuously to the same console.
	time.Sleep(25 * time.Second)
	key, err := os.ReadFile(filepath.Join(home, "private", "id_ed25519.pub"))
	if err != nil {
		return err
	}
	if strings.ContainsAny(strings.TrimSpace(string(key)), "'\r\n") {
		return errors.New("探测公钥格式无效")
	}
	var console bytes.Buffer
	// Keep each line below the guest ash editing buffer; oversized console lines
	// can silently lose their beginning and the completion marker.
	for _, setup := range []string{
		"sleep 5",
		"/etc/init.d/dnsmasq stop; /etc/init.d/odhcpd stop; /etc/init.d/network stop; /etc/init.d/firewall stop",
		"echo 0 > /proc/sys/net/ipv4/ip_forward; ip link set br-lan down 2>/dev/null; ip link set eth0 nomaster 2>/dev/null; true",
		"ip link set eth0 up; ip addr flush dev eth0; ip addr add 10.0.2.15/24 dev eth0; ip route replace default via 10.0.2.2 dev eth0",
		"mkdir -p /etc/dropbear; printf '%s\\n' '" + strings.TrimSpace(string(key)) + "' > /etc/dropbear/authorized_keys; chmod 600 /etc/dropbear/authorized_keys",
		"/etc/init.d/dropbear stop; /usr/sbin/dropbear -p 10.0.2.15:22 -P /tmp/ap-dropbear.pid -s -g; ! pidof dnsmasq odhcpd",
		"ip -4 addr show; ip route show; netstat -lnt; nft list ruleset",
	} {
		console.Reset()
		if err = serialFile(socket, setup, &console); err != nil {
			log.Write(console.Bytes())
			return fmt.Errorf("隔离探测机未就绪，未接入物理网口：%w", err)
		}
		log.Write(console.Bytes())
	}
	// No physical interface exists in the guest before this point.
	if err = prepareNetwork(); err != nil {
		return err
	}
	defer func() {
		if e := rollbackNetwork(); e != nil {
			result = e
		}
	}()
	raw, err := os.ReadFile(networkPending())
	if err != nil {
		return err
	}
	var snapshot networkSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return errors.New("网络恢复记录无效")
	}
	snapshot.ProbeVMPID = vm.Process.Pid
	snapshot.ProbeVMDir = folder
	if err = privateJSON(networkPending(), snapshot); err != nil {
		return err
	}
	for _, item := range []map[string]any{
		{"execute": "netdev_add", "arguments": map[string]any{"type": "vmnet-bridged", "id": "apwire", "ifname": "en0"}},
		{"execute": "netdev_add", "arguments": map[string]any{"type": "hubport", "id": "wireport", "hubid": 78, "netdev": "apwire"}},
	} {
		if err = probeQMP(monitor, item); err != nil {
			return err
		}
	}
	for _, setup := range []string{
		"sleep 2; ip link set eth1 up",
		"printf '%s\\n' '#!/bin/sh' 'case \"$1\" in bound|renew) [ \"$subnet\" = \"255.255.255.0\" ] || exit 1; ip addr flush dev eth1; ip addr add \"$ip/24\" dev eth1;; esac' > /tmp/ap-lease; chmod 700 /tmp/ap-lease",
		"udhcpc -i eth1 -n -q -t 3 -T 2 -s /tmp/ap-lease",
		"ip -4 addr show dev eth1; ip route show; ping -c 2 -W 2 192.168.1.1; ip neigh show dev eth1",
	} {
		console.Reset()
		if err = serialFile(socket, setup, &console); err != nil {
			log.Write(console.Bytes())
			return errors.New("网线侧探测失败，已请求恢复")
		}
		log.Write(console.Bytes())
	}
	neighbors := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(console.String(), "")
	if !strings.Contains(neighbors, "192.168.1.1 lladdr 24:5a:5f:93:a5:71 ") {
		return errors.New("网线侧设备 MAC 不符，未发送登录请求")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: &consoleHTTP{socket: socket}, Jar: jar, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fmt.Println("Verified TP-LINK MAC on isolated Ethernet; Mac Wi-Fi route preserved")
	if routerInspect {
		password, err := readRouterPassword()
		if err != nil || password == "" {
			return errors.New("未读取到 AP 管理凭据")
		}
		routerPassword = password
	}
	return inspectRouter(client, "http://192.168.1.1", false)
}

// The temporary guest is controlled through its private Unix console. HTTP
// request/response envelopes stay in memory; credentials never enter command
// arguments, process listings or saved console diagnostics.
type consoleHTTP struct{ socket string }

func (t *consoleHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != "192.168.1.1" {
		return nil, errors.New("拒绝 AP 以外的目标")
	}
	var body []byte
	if req.Body != nil {
		var e error
		body, e = io.ReadAll(io.LimitReader(req.Body, 1<<20))
		req.Body.Close()
		if e != nil {
			return nil, e
		}
	}
	if len(body) > 1<<18 {
		return nil, errors.New("AP 请求过长")
	}
	var output bytes.Buffer
	run := func(command string) error { output.Reset(); return serialFile(t.socket, command, &output) }
	write := func(path string, data string) error {
		if e := run(": > " + path); e != nil {
			return e
		}
		for i := 0; i < len(data); i += 120 {
			chunk := data[i:min(i+120, len(data))]
			if strings.IndexByte(chunk, 0) >= 0 {
				return errors.New("AP 请求包含空字节")
			}
			quoted := "'" + strings.ReplaceAll(chunk, "'", "'\"'\"'") + "'"
			if e := run("printf '%s' " + quoted + " >> " + path); e != nil {
				return e
			}
		}
		return nil
	}
	if e := write("/tmp/ap-http-body", string(body)); e != nil {
		return nil, e
	}
	args := []string{"silent", "show-error", "max-time = 8", "noproxy = \"*\"", "dump-header = \"/tmp/ap-http-headers\"", "output = \"/tmp/ap-http-response\"", "url = \"" + req.URL.String() + "\"", "request = \"" + req.Method + "\""}
	for key, values := range req.Header {
		for _, value := range values {
			args = append(args, "header = \""+strings.ReplaceAll(key+": "+value, "\"", "\\\"")+"\"")
		}
	}
	if req.Method == "POST" {
		args = append(args, "data-binary = \"@/tmp/ap-http-body\"")
	}
	if e := write("/tmp/ap-http-args", strings.Join(args, "\n")+"\n"); e != nil {
		return nil, e
	}
	if e := run("curl -K /tmp/ap-http-args"); e != nil {
		path := filepath.Join(home, "private/ap-http-error.log")
		os.WriteFile(path, output.Bytes(), 0600)
		ownProbe(path)
		return nil, errors.New("网线侧 AP HTTP 请求失败")
	}
	if e := run("echo AP_RESPONSE_BEGIN; cat /tmp/ap-http-headers; echo; echo AP_BODY_BEGIN; cat /tmp/ap-http-response; echo; echo AP_RESPONSE_END"); e != nil {
		return nil, e
	}
	text := strings.ReplaceAll(output.String(), "\r", "")
	begin := strings.Index(text, "\nAP_RESPONSE_BEGIN\n")
	mid := strings.Index(text, "\nAP_BODY_BEGIN\n")
	end := strings.Index(text, "\nAP_RESPONSE_END\n")
	if begin < 0 || mid < begin || end < mid {
		return nil, errors.New("AP 回应信封无效")
	}
	header := text[begin+len("\nAP_RESPONSE_BEGIN\n") : mid]
	responseBody := strings.TrimSuffix(text[mid+len("\nAP_BODY_BEGIN\n"):end], "\n")
	reader := bufio.NewReader(strings.NewReader(header))
	status, e := reader.ReadString('\n')
	if e != nil {
		return nil, errors.New("AP HTTP 状态缺失")
	}
	parts := strings.SplitN(strings.TrimSpace(status), " ", 3)
	if len(parts) < 2 {
		return nil, errors.New("AP HTTP 状态无效")
	}
	code, e := strconv.Atoi(parts[1])
	if e != nil {
		return nil, e
	}
	headers, e := textproto.NewReader(reader).ReadMIMEHeader()
	if e != nil {
		return nil, e
	}
	return &http.Response{StatusCode: code, Status: strings.TrimPrefix(strings.TrimSpace(status), parts[0]+" "), Header: http.Header(headers), Body: io.NopCloser(strings.NewReader(responseBody)), ContentLength: int64(len(responseBody)), Request: req}, nil
}

func probeQMP(path string, request map[string]any) error {
	c, e := net.DialTimeout("unix", path, time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	d := json.NewDecoder(c)
	var response map[string]any
	if e = d.Decode(&response); e != nil {
		return e
	}
	for _, q := range []map[string]any{{"execute": "qmp_capabilities"}, request} {
		if e = json.NewEncoder(c).Encode(q); e != nil {
			return e
		}
		for {
			if e = d.Decode(&response); e != nil {
				return e
			}
			if _, ok := response["return"]; ok {
				break
			}
			if detail, ok := response["error"]; ok {
				return fmt.Errorf("临时探测网卡请求失败：%v", detail)
			}
		}
	}
	return nil
}
