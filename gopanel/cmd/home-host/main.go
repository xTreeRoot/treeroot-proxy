// home-host manages only the dedicated gateway VM. Network activation is separate.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var home string
var routerInitialize, routerInspect, routerStateOnly, routerPrepareAP bool

func command(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
func alive(pid int) bool { return pid > 1 && syscall.Kill(pid, 0) == nil }
func pid() int {
	b, _ := os.ReadFile(filepath.Join(home, "run/gateway.pid"))
	n, _ := strconv.Atoi(string(b))
	return n
}
func qmp(action string) error {
	c, e := net.DialTimeout("unix", filepath.Join(home, "run/qmp.sock"), 2*time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	d := json.NewDecoder(c)
	var v map[string]any
	if e = d.Decode(&v); e != nil {
		return e
	}
	for _, a := range []string{"qmp_capabilities", action} {
		if e = json.NewEncoder(c).Encode(map[string]string{"execute": a}); e != nil {
			return e
		}
		for {
			if e = d.Decode(&v); e != nil {
				return e
			}
			if _, ok := v["return"]; ok {
				break
			}
			if _, ok := v["error"]; ok {
				return errors.New("QMP rejected command")
			}
		}
	}
	return nil
}
func stop() error {
	lock, e := vmLock()
	if e != nil {
		return e
	}
	defer lock.Close()
	p := pid()
	if !alive(p) {
		return nil
	}
	ssh := exec.Command("/usr/bin/ssh", "-i", filepath.Join(home, "private/id_ed25519"), "-p", "22077", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(home, "private/known_hosts"), "root@127.0.0.1", "sync; poweroff")
	if e := ssh.Run(); e != nil {
		// An SSH disconnect is expected during shutdown. ACPI is only a fallback;
		// never terminate a VM that might still be writing its filesystem.
		qmp("system_powerdown")
	}
	for range 60 {
		if !alive(p) {
			os.Remove(filepath.Join(home, "run/gateway.pid"))
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("VM did not stop; disk was not touched")
}
func start() error { return startVM(false) }
func startVM(supervised bool) error {
	lock, e := vmLock()
	if e != nil {
		return e
	}
	defer lock.Close()
	if supervised {
		if _, e := os.Stat(filepath.Join(home, "run", "paused")); e == nil {
			return nil
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	if alive(pid()) {
		return errors.New("VM already running")
	}
	for _, n := range []string{"serial.sock", "qmp.sock", "ethernet.sock"} {
		os.Remove(filepath.Join(home, "run", n))
	}
	args := []string{"-name", "home-gateway", "-machine", "virt,accel=hvf", "-cpu", "host", "-smp", "2", "-m", "768", "-bios", "/opt/homebrew/share/qemu/edk2-aarch64-code.fd", "-drive", "if=virtio,format=qcow2,file=" + filepath.Join(home, "private/gateway.qcow2"), "-netdev", "user,id=wan,ipv6=off,hostfwd=tcp:127.0.0.1:22077-:22,hostfwd=tcp:127.0.0.1:18077-:80", "-device", "virtio-net-pci,netdev=wan,mac=52:54:00:77:00:01", "-netdev", "hubport,id=lan,hubid=77", "-device", "virtio-net-pci,netdev=lan,mac=52:54:00:77:00:02", "-netdev", "socket,id=lab,listen=127.0.0.1:16377", "-device", "virtio-net-pci,netdev=lab,mac=52:54:00:77:00:03", "-display", "none", "-monitor", "none", "-serial", "unix:" + filepath.Join(home, "run/serial.sock") + ",server=on,wait=off", "-qmp", "unix:" + filepath.Join(home, "run/qmp.sock") + ",server=on,wait=off"}
	// A private, unprivileged endpoint survives guest reboot and is recreated on
	// VM replacement. Only the separately installed vmnet relay reaches en0.
	args = append(args, wireEndpointArgs(home)...)
	log, e := os.OpenFile(filepath.Join(home, "logs/qemu-go.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	c := exec.Command("/opt/homebrew/bin/qemu-system-aarch64", args...)
	c.Stdout = log
	c.Stderr = log
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = c.Start(); e != nil {
		return e
	}
	go c.Wait()
	if e = os.WriteFile(filepath.Join(home, "run/gateway.pid"), []byte(strconv.Itoa(c.Process.Pid)), 0600); e != nil {
		return e
	}
	os.WriteFile(filepath.Join(home, "run/mode"), []byte("isolated"), 0600)
	return nil
}
func copyFile(src, dst string) error {
	a, e := os.Open(src)
	if e != nil {
		return e
	}
	defer a.Close()
	b, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(b, a)
	if ce := b.Close(); e == nil {
		e = ce
	}
	return e
}
func repair() error {
	if e := stop(); e != nil {
		return e
	}
	disk := filepath.Join(home, "private/gateway.qcow2")
	backup := disk + ".before-go-repair-" + time.Now().UTC().Format("20060102T150405Z")
	if e := copyFile(disk, backup); e != nil {
		return e
	}
	folder, e := os.MkdirTemp(filepath.Join(home, "private"), "repair-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(folder)
	raw := filepath.Join(folder, "disk.raw")
	if e = command("/opt/homebrew/bin/qemu-img", "convert", "-f", "qcow2", "-O", "raw", disk, raw); e != nil {
		return e
	}
	f, e := os.OpenFile(raw, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	header := make([]byte, 512)
	if _, e = f.ReadAt(header, 512); e != nil {
		return e
	}
	if string(header[:8]) != "EFI PART" {
		return errors.New("disk has no GPT")
	}
	offset := binary.LittleEndian.Uint64(header[72:80]) * 512
	size := binary.LittleEndian.Uint32(header[84:88])
	if size < 128 || size > 4096 {
		return errors.New("invalid GPT entry size")
	}
	entry := make([]byte, size)
	if _, e = f.ReadAt(entry, int64(offset)+int64(size)); e != nil {
		return e
	}
	first, end := binary.LittleEndian.Uint64(entry[32:40]), binary.LittleEndian.Uint64(entry[40:48])
	stat, _ := f.Stat()
	if first < 2048 || end <= first || end*512 >= uint64(stat.Size()) {
		return errors.New("invalid root partition")
	}
	part := filepath.Join(folder, "root.ext4")
	out, e := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.CopyN(out, io.NewSectionReader(f, int64(first*512), int64((end-first+1)*512)), int64((end-first+1)*512))
	out.Close()
	if e != nil {
		return e
	}
	e = command("/opt/homebrew/opt/e2fsprogs/sbin/e2fsck", "-f", "-p", part)
	if exit, ok := e.(*exec.ExitError); e != nil && (!ok || exit.ExitCode() != 1) {
		return e
	}
	if e = command("/opt/homebrew/opt/e2fsprogs/sbin/resize2fs", part); e != nil {
		return e
	}
	if e = command("/opt/homebrew/opt/e2fsprogs/sbin/e2fsck", "-f", "-p", part); e != nil {
		if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			return e
		}
	}
	if e = command("/opt/homebrew/opt/e2fsprogs/sbin/e2fsck", "-f", "-n", part); e != nil {
		return e
	}
	p, e := os.Open(part)
	if e != nil {
		return e
	}
	defer p.Close()
	if _, e = f.Seek(int64(first*512), 0); e != nil {
		return e
	}
	if _, e = io.Copy(f, p); e != nil {
		return e
	}
	f.Sync()
	candidate := filepath.Join(folder, "gateway.qcow2")
	if e = command("/opt/homebrew/bin/qemu-img", "convert", "-f", "raw", "-O", "qcow2", raw, candidate); e != nil {
		return e
	}
	if e = command("/opt/homebrew/bin/qemu-img", "check", candidate); e != nil {
		return e
	}
	if e = os.Chmod(candidate, 0600); e != nil {
		return e
	}
	if e = os.Rename(candidate, disk); e != nil {
		return e
	}
	fmt.Println("Disk checked and expanded; original private snapshot preserved")
	return start()
}
func main() {
	flag.StringVar(&home, "home", "", "gateway directory")
	console := flag.String("console", "serial.sock", "local VM console socket")
	cmd := flag.String("command", "", "local VM console command")
	destination := flag.String("destination", "", "new private isolated runtime directory")
	flag.Parse()
	if home == "" || !filepath.IsAbs(home) {
		fmt.Fprintln(os.Stderr, "absolute -home is required")
		os.Exit(2)
	}
	switch flag.Arg(0) {
	case "status", "start", "stop", "repair", "supervise", "install-service":
		if target, err := activeRuntime(home); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		} else {
			home = target
		}
	case "serial":
		if *console == "serial.sock" {
			if target, err := activeRuntime(home); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			} else {
				home = target
			}
		}
	}
	var e error
	switch flag.Arg(0) {
	case "install-wire-service":
		e = installWireService()
	case "wire-service":
		e = wireService()
	case "attach-wire":
		e = attachWire()
	case "router-vm-login":
		routerInspect = true
		e = inspectRouterVM()
	case "inspect-router-vm":
		e = inspectRouterVM()
	case "prepare-router-ap":
		routerPrepareAP = true
		e = inspectRouterVM()
	case "router-state":
		routerStateOnly = true
		e = probeCable()
	case "relocate-runtime":
		e = relocateRuntime(*destination)
	case "install-service":
		e = installService()
	case "supervise":
		e = supervise()
	case "router-login":
		routerPassword, e = readRouterPassword()
		if e == nil && routerPassword != "" {
			routerInspect = true
			e = probeCable()
		}
	case "initialize-router":
		routerInitialize = true
		e = probeCable()
	case "inspect-router":
		routerInspect = true
		e = probeCable()
	case "cleanup-network":
		e = cleanupNetwork()
	case "probe-cable":
		e = probeCable()
	case "snapshot-network":
		e = snapshotNetwork()
	case "prepare-network":
		e = prepareNetwork()
	case "watch-network":
		e = watchNetwork()
	case "rollback-network":
		e = rollbackNetwork()
	case "confirm-network":
		e = confirmNetwork()
	case "serial":
		e = serial(*console, *cmd)
	case "lab-start":
		e = labStart()
	case "status":
		mode, _ := os.ReadFile(filepath.Join(home, "run/mode"))
		fmt.Printf("running=%t mode=%s\n", alive(pid()), strings.TrimSpace(string(mode)))
	case "start":
		os.Remove(filepath.Join(home, "run", "paused"))
		e = start()
	case "stop":
		e = os.WriteFile(filepath.Join(home, "run", "paused"), []byte("manual stop\n"), 0600)
		if e == nil {
			e = stop()
		}
	case "repair":
		e = os.WriteFile(filepath.Join(home, "run", "paused"), []byte("disk maintenance\n"), 0600)
		if e == nil {
			e = repair()
			if e == nil {
				os.Remove(filepath.Join(home, "run", "paused"))
			}
		}
	default:
		e = errors.New("choose status, start, stop or repair")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
