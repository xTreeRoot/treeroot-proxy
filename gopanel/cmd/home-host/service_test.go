package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupervisorHonorsMaintenancePause(t *testing.T) {
	previous := home
	home = t.TempDir()
	t.Cleanup(func() { home = previous })
	if e := os.Mkdir(filepath.Join(home, "run"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "run/paused"), []byte("maintenance"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := startVM(true); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(home, "run/gateway.pid")); !os.IsNotExist(e) {
		t.Fatal("paused supervisor attempted to start a VM")
	}
	lock, e := vmLock()
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if second, e := vmLock(); e == nil {
		second.Close()
		t.Fatal("overlapping VM operations acquired the same lock")
	}
}

func TestSupervisorRefusesPhysicalOrUnrelatedVM(t *testing.T) {
	dir := "/home/user/a gateway"
	valid := "/opt/homebrew/bin/qemu-system-aarch64 -name home-gateway -drive if=virtio,format=qcow2,file=" + dir + "/private/gateway.qcow2 -netdev user,id=wan,hostfwd=tcp:127.0.0.1:22077-:22 "
	for _, test := range []struct {
		name, cmd string
		want      bool
	}{
		{"verified-isolated", valid, true},
		{"wrong-directory", valid + "", false},
		{"physical-bridge", valid + " -netdev vmnet-bridged,id=lan,ifname=en0", false},
		{"physical-tap", valid + " -netdev tap,id=lan", false},
		{"different-process", "/bin/sleep 1000", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := dir
			if test.name == "wrong-directory" {
				directory = "/home/another"
			}
			if got := isolatedVMCommand(test.cmd, directory); got != test.want {
				t.Fatalf("adopt=%t want %t", got, test.want)
			}
		})
	}
}
