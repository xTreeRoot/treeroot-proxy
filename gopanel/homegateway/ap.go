package homegateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"treeroot/gopanel/homeap"
)

type APConfig struct {
	Address  string `json:"address"`
	MAC      string `json:"mac"`
	Password string `json:"password"`
}
type apRecovery struct {
	ID       string         `json:"id"`
	Deadline int64          `json:"deadline"`
	Before   map[string]any `json:"before"`
}

func (a *Agent) ap() (*homeap.TPLink, error) {
	if a.Config.AP == nil {
		return nil, errors.New("尚未配置已核实的 AP")
	}
	c := a.Config.AP
	return &homeap.TPLink{Base: "http://" + c.Address, Password: c.Password, MAC: c.MAC, Client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *Agent) APStatus(ctx context.Context) (homeap.WiFi, error) {
	t, e := a.ap()
	if e != nil {
		return homeap.WiFi{}, e
	}
	if e = t.Login(ctx); e != nil {
		return homeap.WiFi{}, e
	}
	raw, e := t.Snapshot(ctx)
	if e != nil {
		return homeap.WiFi{}, e
	}
	view := homeap.View(raw)
	a.State.WiFi = &view
	a.apChecked = time.Now()
	return view, nil
}
func (a *Agent) apRecoveryPath() string { return filepath.Join(a.Config.StateDir, "ap-pending.json") }
func (a *Agent) RecoverAP(ctx context.Context, id string, wait bool) error {
	raw, e := os.ReadFile(a.apRecoveryPath())
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var journal apRecovery
	if json.Unmarshal(raw, &journal) != nil {
		return errors.New("AP 回退记录无效")
	}
	if id != "" && journal.ID != id {
		return nil
	}
	if wait {
		timer := time.NewTimer(max(0, time.Until(time.Unix(journal.Deadline, 0))))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		raw, e = os.ReadFile(a.apRecoveryPath())
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		var current apRecovery
		if json.Unmarshal(raw, &current) != nil || current.ID != journal.ID {
			return nil
		}
	}
	t, e := a.ap()
	if e != nil {
		return e
	}
	if e = t.Login(ctx); e != nil {
		return e
	}
	if e = t.Restore(ctx, journal.Before); e != nil {
		return e
	}
	return os.Remove(a.apRecoveryPath())
}
func (a *Agent) beginAPRecovery(before map[string]any) (apRecovery, error) {
	var journal apRecovery
	if _, e := os.Stat(a.apRecoveryPath()); e == nil {
		return journal, errors.New("上次无线调整尚未恢复")
	}
	journal = apRecovery{Nonce(), time.Now().Add(90 * time.Second).Unix(), before}
	raw, e := json.Marshal(journal)
	if e != nil {
		return journal, e
	}
	if e = Atomic(a.apRecoveryPath(), raw, 0600); e != nil {
		return journal, e
	}
	binary, e := os.Executable()
	if e != nil {
		return journal, e
	}
	p := exec.Command(binary, "-config", a.ConfigPath, "-transaction", journal.ID, "ap-recover-watch")
	p.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = p.Start(); e != nil {
		os.Remove(a.apRecoveryPath())
		return journal, e
	}
	go p.Wait()
	return journal, nil
}
func (a *Agent) SetWiFi(ctx context.Context, s homeap.Settings, restart bool) error {
	t, e := a.ap()
	if e != nil {
		return e
	}
	if e = t.Login(ctx); e != nil {
		return e
	}
	before, e := t.Snapshot(ctx)
	if e != nil {
		return e
	}
	if !restart {
		if e = s.Validate(); e != nil {
			return e
		}
	}
	journal, e := a.beginAPRecovery(before)
	if e != nil {
		return e
	}
	backup, _ := json.Marshal(before)
	if e = Atomic(filepath.Join(a.Config.StateDir, "backups", "ap-"+journal.ID+".json"), backup, 0600); e != nil {
		return e
	}
	success := false
	defer func() {
		if !success {
			restore, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			a.RecoverAP(restore, journal.ID, false)
		}
	}()
	if restart {
		// Use the same documented enable fields, then restore the complete prior
		// wireless configuration over the wired management path.
		_, e = t.Request(ctx, map[string]any{"method": "set", "wireless": map[string]any{"wlan_bs": map[string]any{"wifi_enable": 0}, "wlan_host_2g": map[string]any{"enable": 0}, "wlan_host_5g": map[string]any{"enable": 0}}})
		if e == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			e = t.Restore(ctx, before)
		}
	} else {
		e = t.Set(ctx, s, before)
	}
	if e != nil {
		return e
	}
	for range 15 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		check, err := a.ap()
		if err != nil {
			return err
		}
		if err = check.Login(ctx); err != nil {
			continue
		}
		raw, err := check.Snapshot(ctx)
		if err != nil {
			continue
		}
		view := homeap.View(raw)
		a.State.WiFi = &view
		a.apChecked = time.Now()
		want := s
		if restart {
			old := homeap.View(before)
			want.Unified = old.Unified
			want.Band2 = old.Band2
			want.Band5 = old.Band5
		}
		match := func(got, w homeap.Band) bool {
			return got.Enabled == w.Enabled && got.SSID == w.SSID && got.Channel == w.Channel
		}
		if (restart && view.Unified == want.Unified && match(view.Band2, want.Band2) && (view.Unified || match(view.Band5, want.Band5))) || (!restart && homeap.Matches(raw, s)) {
			if e = os.Remove(a.apRecoveryPath()); e != nil {
				return e
			}
			success = true
			return a.save()
		}
	}
	return errors.New("无线实际配置未确认，已请求回退")
}
func (a *Agent) RebootAP(ctx context.Context) error {
	t, e := a.ap()
	if e != nil {
		return e
	}
	if e = t.Login(ctx); e != nil {
		return e
	}
	if e = t.Reboot(ctx); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(8 * time.Second):
	}
	for {
		if _, e = a.APStatus(ctx); e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("AP 尚未恢复在线")
		case <-time.After(3 * time.Second):
		}
	}
}
