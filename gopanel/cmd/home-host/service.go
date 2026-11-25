package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func fileLock(name string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(home, "run", name), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("家庭网关操作正在运行")
	}
	return f, nil
}

func vmLock() (*os.File, error) { return fileLock("vm-start.lock") }

// Refuse to adopt unrelated or physical-mode VMs. This supervisor is deliberately
// limited to the isolated guest until the AP and the physical LAN are verified.
func isolatedVMCommand(cmd, directory string) bool {
	return strings.Contains(cmd, "/qemu-system-aarch64 ") &&
		strings.Contains(cmd, "-name home-gateway ") &&
		strings.Contains(cmd, "file="+filepath.Join(directory, "private/gateway.qcow2")+" ") &&
		!strings.Contains(cmd, "vmnet-") && !strings.Contains(cmd, "tap,") &&
		strings.Contains(cmd, "hostfwd=tcp:127.0.0.1:22077-:22")
}

func verifyRunningVM() error {
	if !alive(pid()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command, e := exec.CommandContext(ctx, "/bin/ps", "-p", fmt.Sprint(pid()), "-o", "command=").Output()
	if e != nil || !isolatedVMCommand(string(command), home) {
		return errors.New("记录的进程不是已核实的隔离网关，未启动或停止任何进程")
	}
	return nil
}

func supervise() error {
	lock, e := fileLock("supervisor.lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// -w lets caffeinate terminate if this supervisor exits unexpectedly.
	awake := exec.Command("/usr/bin/caffeinate", "-is", "-w", fmt.Sprint(os.Getpid()))
	if e = awake.Start(); e != nil {
		return errors.New("无法启动家庭网关运行期间的防休眠进程")
	}
	defer func() { awake.Process.Kill(); awake.Wait() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if _, e := os.Stat(filepath.Join(home, "run", "paused")); os.IsNotExist(e) {
			if e = verifyRunningVM(); e != nil {
				return e
			}
			if !alive(pid()) {
				mode, _ := os.ReadFile(filepath.Join(home, "run", "mode"))
				if strings.TrimSpace(string(mode)) != "isolated" {
					return errors.New("自动恢复仅支持已核实的隔离模式")
				}
				if e = startVM(true); e != nil {
					return e
				}
				fmt.Println("Isolated gateway VM restarted")
			}
		} else if e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			// launchd may restart only the supervisor; never power-cycle a healthy VM.
			return nil
		case <-ticker.C:
		}
	}
}
