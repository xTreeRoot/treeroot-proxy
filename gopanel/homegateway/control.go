package homegateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"treeroot/gopanel/homeap"
)

func (a *Agent) serverRequest(ctx context.Context, path string, body any, reply any) error {
	data, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", a.Config.Server+"/api/home/"+a.Config.ID+path, bytes.NewReader(data))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.Config.Token)
	res, e := a.client.Do(req)
	if e != nil {
		return errors.New("管理通道连接失败，继续使用最后有效配置")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("管理通道返回 %d，继续使用最后有效配置", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(reply)
}
func (a *Agent) Pair(ctx context.Context, code string) error {
	var reply struct {
		OK bool `json:"ok"`
	}
	if e := a.serverRequest(ctx, "/pair", map[string]string{"code": code, "token": a.Config.Token}, &reply); e != nil {
		return e
	}
	if !reply.OK {
		return errors.New("配对未确认")
	}
	return nil
}
func (a *Agent) Run(ctx context.Context) error {
	lock, e := a.Lock()
	if e != nil {
		return e
	}
	defer lock.Close()
	if e := a.Recover(ctx, "", false); e != nil {
		return e
	}
	if e := a.RecoverAP(ctx, "", false); e != nil {
		a.State.Error = "AP 配置回退待恢复"
	}
	a.State.Boot = Nonce()
	a.State.Sequence = 0
	if a.State.Applied > 0 {
		if e := a.ApplyLocal(ctx, a.State.Desired); e != nil {
			return e
		}
	}
	events := make(chan Event, 1024)
	go a.stream(ctx, events)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	// A frame is retained unchanged until the server acknowledges it.
	var pending *Sync
	for {
		if e := a.sample(ctx); e != nil {
			return errors.New("无法持久化流量账本，管理程序已停止")
		}
		devices := a.discover(ctx)
		for i, d := range devices {
			m := a.ledger.Meters[d.MAC]
			devices[i].Upload = m.Upload
			devices[i].Download = m.Download
		}
		a.State.Devices = devices
		if a.State.Applied > 0 && !sameBindings(a.State.Desired, devices, a.boundDevices) {
			if e := a.ApplyLocal(ctx, a.State.Desired); e != nil {
				a.State.Error = e.Error()
			}
		}
		for len(a.State.Events) < 10000 {
			select {
			case e := <-events:
				for _, d := range devices {
					if d.IP == e.Source {
						e.MAC = d.MAC
						break
					}
				}
				if e.MAC != "" {
					a.State.Events = append(a.State.Events, e)
				}
			default:
				goto drained
			}
		}
	drained:
		if e := a.save(); e != nil {
			return e
		}
		if pending == nil {
			a.State.Sequence++
			pending = &Sync{Boot: a.State.Boot, Sequence: a.State.Sequence, Version: Version, Applied: a.State.Applied, Error: strings.TrimSpace(a.State.Error + " " + a.State.ControlError), Status: a.Diagnostics(ctx), Capabilities: Capabilities{Routing: true, DHCP: true, Limits: false, WiFi: a.Config.AP != nil, APReboot: a.Config.AP != nil, Upgrade: false, IPv6: "blocked_on_lan", Mode: a.Config.Mode}, Devices: slices.Clone(devices), Events: slices.Clone(a.State.Events[:min(50, len(a.State.Events))]), Results: slices.Clone(a.State.Results)}
		}
		var reply Reply
		err := a.serverRequest(ctx, "/sync", pending, &reply)
		if err != nil {
			a.State.ChannelError = err.Error()
		} else if reply.Accepted != pending.Sequence {
			a.State.ChannelError = "管理通道未确认当前帧"
		}
		if err == nil && reply.Accepted == pending.Sequence {
			a.State.ChannelError = ""
			a.State.LastSync = time.Now().Unix()
			a.State.Events = a.State.Events[len(pending.Events):]
			a.State.Results = nil
			pending = nil
			a.State.ControlError = reply.ConfigError
			before, _ := json.Marshal(a.State.Desired)
			after, _ := json.Marshal(reply.Config)
			if reply.Config != nil && (reply.Config.Revision > a.State.Applied || reply.Config.Revision == a.State.Applied && !bytes.Equal(before, after)) {
				digest := fmt.Sprintf("%x", sha256.Sum256(after))
				if digest != a.State.FailedConfig || time.Now().Unix() >= a.State.RetryAfter {
					if e = a.ApplyLocal(ctx, *reply.Config); e != nil {
						if digest != a.State.FailedConfig {
							a.State.Failures = 0
						}
						a.State.Failures++
						a.State.FailedConfig = digest
						a.State.RetryAfter = time.Now().Add(time.Duration(min(a.State.Failures, 5)) * time.Minute).Unix()
						a.State.Error = e.Error()
					}
				}
			}
			for _, task := range reply.Tasks {
				a.execute(ctx, task)
			}
			if e = a.save(); e != nil {
				return e
			}
		}
		select {
		case <-ctx.Done():
			return a.save()
		case <-ticker.C:
		}
	}
}
func sameBindings(c Config, a, b []Device) bool {
	bound := func(list []Device) map[string]string {
		m := map[string]string{}
		for _, d := range bindPolicies(c, list) {
			if d.IP != "" {
				m[d.MAC] = d.IP
			}
		}
		return m
	}
	l, r := bound(a), bound(b)
	if len(l) != len(r) {
		return false
	}
	for k, v := range l {
		if r[k] != v {
			return false
		}
	}
	return true
}
func (a *Agent) execute(ctx context.Context, t Task) {
	if done, ok := a.State.Completed[t.ID]; ok {
		a.State.Results = append(a.State.Results, done)
		return
	}
	result := Result{ID: t.ID, Kind: t.Kind, State: "failed"}
	if t.Expires < time.Now().Unix() || t.Created > time.Now().Unix()+30 {
		result.State = "expired"
		result.Detail = "任务已过期"
	} else {
		// Persist the claim before executing a disruptive action. Replays cannot restart twice.
		result.State = "running"
		result.Detail = "任务已开始"
		a.State.Completed[t.ID] = result
		if e := a.save(); e != nil {
			return
		}
		duration := 25 * time.Second
		if t.Kind == "wifi.set" || t.Kind == "wifi.restart" || t.Kind == "ap.reboot" {
			duration = 75 * time.Second
		}
		taskCtx, cancel := context.WithTimeout(ctx, duration)
		var e error
		switch t.Kind {
		case "wifi.set":
			var settings homeap.Settings
			if json.Unmarshal([]byte(t.Payload["settings"]), &settings) != nil {
				e = errors.New("无线设置格式无效")
			} else {
				e = a.SetWiFi(taskCtx, settings, false)
			}
			result.Detail = "无线实际配置已确认"
		case "wifi.restart":
			e = a.SetWiFi(taskCtx, homeap.Settings{}, true)
			result.Detail = "无线已重启并恢复配置"
		case "ap.reboot":
			e = a.RebootAP(taskCtx)
			result.Detail = "Wi-Fi AP 已重启并恢复在线"
		case "proxy.restart":
			e = a.restartProxy(taskCtx)
			if e == nil {
				e = a.waitProxy(taskCtx)
			}
		case "diagnostics":
			result.Detail = "家庭网关诊断已更新"
		case "backup.create":
			var raw []byte
			if t.Payload["revision"] != fmt.Sprint(a.State.Applied) {
				e = errors.New("待备份配置尚未应用")
			} else {
				raw, e = json.Marshal(a.State.Desired)
			}
			if e == nil {
				e = Atomic(filepath.Join(a.Config.StateDir, "backups", t.ID+".json"), raw, 0600)
			}
			result.Detail = "备份编号：" + t.ID
		case "backup.restore":
			id := t.Payload["backup_id"]
			if len(id) != 32 || !validHex(id) {
				e = errors.New("备份编号无效")
				break
			}
			var raw []byte
			raw, e = os.ReadFile(filepath.Join(a.Config.StateDir, "backups", id+".json"))
			if e == nil {
				var c Config
				e = json.Unmarshal(raw, &c)
				if e == nil {
					// The server publishes the historical policy as a new revision.
					// Verify it rather than overwrite it with the old local revision.
					want := c
					want.Revision = a.State.Desired.Revision
					want.Nodes = a.State.Desired.Nodes
					x, _ := json.Marshal(want)
					y, _ := json.Marshal(a.State.Desired)
					if !bytes.Equal(x, y) {
						e = errors.New("备份策略尚未恢复")
					}
				}
			}
		case "gateway.reboot":
			result.Detail = "已发送重启，等待新启动确认"
			a.State.Completed[t.ID] = result
			a.State.Results = append(a.State.Results, result)
			if a.save() != nil {
				cancel()
				return
			}
			_, e = run(taskCtx, "/sbin/reboot")
		default:
			e = errors.New("当前硬件或程序不支持此操作")
		}
		cancel()
		if e != nil {
			result.State = "failed"
			result.Detail = "操作未完成：" + safeTaskError(t.Kind)
		} else if t.Kind != "gateway.reboot" {
			result.State = "success"
			if result.Detail == "任务已开始" {
				result.Detail = "已执行并验证"
			}
		}
	}
	a.State.Completed[t.ID] = result
	a.State.Results = append(a.State.Results, result)
	// Completed claims are retained so retries remain idempotent across reboots.
	a.save()
}
func validHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func safeTaskError(kind string) string {
	switch kind {
	case "backup.restore":
		return "备份读取或应用失败"
	case "proxy.restart":
		return "代理未恢复健康"
	default:
		return "家庭网关执行失败"
	}
}
func (a *Agent) waitProxy(ctx context.Context) error {
	return a.waitConfiguration(ctx, a.State.Desired)
}

func (a *Agent) restartProxy(ctx context.Context) error {
	if _, e := run(ctx, "/etc/init.d/home-mihomo", "stop"); e != nil {
		return e
	}
	stopped := false
	for range 50 {
		probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		var version map[string]any
		e := a.apiRequest(probe, "GET", "/version", nil, &version)
		cancel()
		if e != nil {
			stopped = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !stopped {
		return errors.New("旧代理进程尚未停止")
	}
	_, e := run(ctx, "/etc/init.d/home-mihomo", "start")
	return e
}
