package homegateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type recovery struct {
	ID       string `json:"id"`
	Deadline int64  `json:"deadline"`
	Base     string `json:"base"`
	Mihomo   []byte `json:"mihomo"`
	DHCP     []byte `json:"dhcp"`
	NFT      []byte `json:"nft"`
	HadRule  bool   `json:"had_rule"`
	State    []byte `json:"state"`
	Usage    []byte `json:"usage"`
}

func (a *Agent) recoveryPath() string {
	return filepath.Join(a.Config.StateDir, "pending-recovery.json")
}
func nftRun(ctx context.Context, input string, check bool) error {
	args := []string{"-f", "-"}
	if check {
		args = append([]string{"-c"}, args...)
	}
	cmd := exec.CommandContext(ctx, "nft", args...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}
func (a *Agent) beginRecovery(ctx context.Context) (recovery, error) {
	var r recovery
	if _, e := os.Stat(a.recoveryPath()); e == nil {
		return r, errors.New("上次配置尚未完成回退")
	}
	var e error
	r.ID = Nonce()
	r.Deadline = time.Now().Add(90 * time.Second).Unix()
	r.Base = a.Config.Base
	r.State, e = json.Marshal(a.State)
	if e != nil {
		return r, e
	}
	r.Usage, _ = os.ReadFile(filepath.Join(a.Config.StateDir, "usage.json"))
	r.Mihomo, e = os.ReadFile(r.Base)
	if e != nil {
		return r, e
	}
	r.DHCP, e = os.ReadFile("/etc/config/dhcp")
	if e != nil {
		return r, e
	}
	r.NFT, _ = run(ctx, "nft", "list", "table", "inet", "treeroot_home")
	rules, e := run(ctx, "ip", "rule", "show")
	if e != nil {
		return r, e
	}
	r.HadRule = strings.Contains(string(rules), "fwmark 0x77 lookup 177")
	raw, _ := json.Marshal(r)
	if e = Atomic(a.recoveryPath(), raw, 0600); e != nil {
		return r, e
	}
	executable, e := os.Executable()
	if e != nil {
		return r, e
	}
	cmd := exec.Command(executable, "-config", a.ConfigPath, "-transaction", r.ID, "recover-watch")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		os.Remove(a.recoveryPath())
		return r, e
	}
	go cmd.Wait()
	return r, nil
}
func (a *Agent) restore(ctx context.Context, r recovery) error {
	// The private journal is created locally, never accepted from the control server.
	if r.Base != a.Config.Base {
		return errors.New("恢复路径不匹配")
	}
	if e := Atomic(r.Base, r.Mihomo, 0600); e != nil {
		return e
	}
	if e := Atomic("/etc/config/dhcp", r.DHCP, 0600); e != nil {
		return e
	}
	table := "add table inet treeroot_home\ndelete table inet treeroot_home\n" + string(r.NFT)
	if e := nftRun(ctx, table, false); e != nil {
		return e
	}
	if !r.HadRule {
		run(ctx, "ip", "rule", "del", "priority", "177", "fwmark", "0x77", "lookup", "177")
		run(ctx, "ip", "route", "flush", "table", "177")
	}
	if _, e := run(ctx, "/etc/init.d/home-mihomo", "restart"); e != nil {
		return e
	}
	if _, e := run(ctx, "/etc/init.d/dnsmasq", "restart"); e != nil {
		return e
	}
	if len(r.State) > 0 {
		if e := Atomic(filepath.Join(a.Config.StateDir, "state.json"), r.State, 0600); e != nil {
			return e
		}
		json.Unmarshal(r.State, &a.State)
	}
	if len(r.Usage) > 0 {
		if e := Atomic(filepath.Join(a.Config.StateDir, "usage.json"), r.Usage, 0600); e != nil {
			return e
		}
		json.Unmarshal(r.Usage, &a.ledger)
	}
	return os.Remove(a.recoveryPath())
}
func (a *Agent) Recover(ctx context.Context, transaction string, wait bool) error {
	raw, e := os.ReadFile(a.recoveryPath())
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var r recovery
	if json.Unmarshal(raw, &r) != nil {
		return errors.New("回退记录损坏，拒绝应用新配置")
	}
	if transaction != "" && r.ID != transaction {
		return nil
	}
	if wait {
		delay := time.Until(time.Unix(r.Deadline, 0))
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
		raw, e = os.ReadFile(a.recoveryPath())
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		var current recovery
		if json.Unmarshal(raw, &current) != nil || current.ID != r.ID {
			return nil
		}
	}
	return a.restore(ctx, r)
}
