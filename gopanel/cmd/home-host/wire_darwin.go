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

// The root process owns only vmnet, while the gateway VM stays under the user.
// The Unix stream is private to this runtime and cannot be reached from LAN.
func attachWire() error {
	if e := requireRoot(); e != nil {
		return e
	}
	runtime, e := activeRuntime(home)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	route, e := hostOutput(ctx, "/sbin/route", "-n", "get", "default")
	if e != nil || !strings.Contains(route, "interface: en1") {
		return errors.New("Wi-Fi 默认路由未保持，未接入网线")
	}
	socket := filepath.Join(runtime, "run", "ethernet.sock")
	monitor := filepath.Join(runtime, "run", "qmp.sock")
	if _, e = os.Stat(socket); os.IsNotExist(e) {
		for _, item := range []map[string]any{
			{"execute": "netdev_add", "arguments": map[string]any{"type": "stream", "id": "physicalwire", "addr": map[string]any{"type": "unix", "path": socket}, "server": true}},
			{"execute": "netdev_add", "arguments": map[string]any{"type": "hubport", "id": "physicalhub", "hubid": 77, "netdev": "physicalwire"}},
		} {
			if e = probeQMP(monitor, item); e != nil {
				return e
			}
		}
	}
	fmt.Println("Private Ethernet relay active; gateway LAN interface controls forwarding")
	process := exec.Command("/opt/homebrew/bin/qemu-system-aarch64", relayArgs(runtime)...)
	process.Stdout, process.Stderr = os.Stdout, os.Stderr
	return process.Run()
}

func wireService() error {
	if e := requireRoot(); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(wireInstallDir, "relay.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("物理中继已运行")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for {
		// Never enable DHCP or replace the Mac default route. Wait for the
		// explicitly verified manual LAN configuration and the user VM.
		s, e := networkSnapshotNow()
		if e == nil && fixedEthernetBaseline(s) && strings.Contains(s.DefaultRoute, "interface: en1") {
			if _, e = os.Stat(filepath.Join(home, "run/ethernet.sock")); e == nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	args := append([]string{"-L", wireInstallDir}, relayArgs(home)...)
	process := exec.CommandContext(ctx, filepath.Join(wireInstallDir, "qemu-system-aarch64"), args...)
	// Do not load executable modules, resources, or dylibs from Homebrew or
	// inherit a caller's DYLD/QEMU overrides in a root launch daemon.
	process.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "QEMU_MODULE_DIR=" + filepath.Join(wireInstallDir, "modules")}
	process.Stdout, process.Stderr = os.Stdout, os.Stderr
	process.Cancel = func() error { return process.Process.Signal(syscall.SIGTERM) }
	process.WaitDelay = 5 * time.Second
	fmt.Println("Physical Ethernet relay started")
	e = process.Run()
	if ctx.Err() != nil {
		return nil
	}
	return e
}
