package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

func serial(socket, command string) error {
	return serialOutput(socket, command, os.Stdout)
}
func serialOutput(socket, command string, output io.Writer) error {
	return serialFile(filepath.Join(home, "run", socket), command, output)
}
func serialFile(path, command string, output io.Writer) error {
	c, e := net.DialTimeout("unix", path, time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	readyDeadline := time.Now().Add(60 * time.Second)
	c.SetDeadline(readyDeadline)
	// Never send Ctrl-C or a shell command into a booting guest: GRUB interprets
	// those bytes as boot-menu input. Wait for the actual OpenWrt root prompt.
	if _, e = c.Write([]byte("\r")); e != nil {
		return e
	}
	buf := make([]byte, 4096)
	var greeting []byte
	activated := false
	for !rootPrompt(greeting) {
		c.SetReadDeadline(minTime(readyDeadline, time.Now().Add(2*time.Second)))
		n, err := c.Read(buf)
		greeting = append(greeting, buf[:n]...)
		if bytes.Contains(greeting, []byte("grub>")) {
			output.Write(greeting)
			return errors.New("测试机停在引导器，未发送诊断命令")
		}
		if !activated && bytes.Contains(greeting, []byte("Please press Enter to activate this console.")) {
			if _, e = c.Write([]byte("\r")); e != nil {
				return e
			}
			activated = true
		}
		if err != nil && time.Now().Before(readyDeadline) {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				if activated {
					if _, e = c.Write([]byte("\r")); e != nil {
						return e
					}
				}
				continue
			}
		}
		if err != nil {
			output.Write(greeting)
			return errors.New("尚未等到 OpenWrt 控制台，未发送诊断命令")
		}
		if len(greeting) > 256<<10 {
			return errors.New("控制台就绪输出过长，未发送诊断命令")
		}
	}
	c.SetDeadline(time.Now().Add(45 * time.Second))
	var nonce [8]byte
	rand.Read(nonce[:])
	marker := "HG_" + hex.EncodeToString(nonce[:])
	payload := command + "; printf '\\n" + marker + ":%s\\n' \"$?\"\r"
	for i := 0; i < len(payload); i += 4 {
		if _, e = c.Write([]byte(payload[i:min(i+4, len(payload))])); e != nil {
			return e
		}
		time.Sleep(25 * time.Millisecond)
	}
	var data []byte
	completed := regexp.MustCompile(`\n` + marker + `:(\d+)\r?\n`)
	for {
		n, e := c.Read(buf)
		data = append(data, buf[:n]...)
		if match := completed.FindSubmatch(data); match != nil {
			fmt.Fprint(output, string(data))
			if string(match[1]) != "0" {
				return fmt.Errorf("隔离控制台命令返回 %s", match[1])
			}
			return nil
		}
		if e != nil {
			fmt.Fprint(output, string(data))
			return e
		}
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func rootPrompt(data []byte) bool {
	line := data
	if n := bytes.LastIndexByte(data, '\n'); n >= 0 {
		line = data[n+1:]
	}
	return bytes.HasPrefix(line, []byte("root@")) && bytes.HasSuffix(bytes.TrimSpace(line), []byte("#"))
}
func labStart() error {
	if raw, e := os.ReadFile(filepath.Join(home, "run/lab.pid")); e == nil {
		p, _ := strconv.Atoi(string(raw))
		if alive(p) {
			return errors.New("lab VM already running")
		}
	}
	disk := filepath.Join(home, "private/go-lab-client.qcow2")
	if _, e := os.Stat(disk); os.IsNotExist(e) {
		if e = copyFile(filepath.Join(home, "private/test-client.qcow2"), disk); e != nil {
			return e
		}
	}
	os.Remove(filepath.Join(home, "run/go-lab.sock"))
	args := []string{"-name", "home-go-lab-client", "-machine", "virt,accel=hvf", "-cpu", "host", "-smp", "1", "-m", "256", "-bios", "/opt/homebrew/share/qemu/edk2-aarch64-code.fd", "-drive", "if=virtio,format=qcow2,file=" + disk, "-netdev", "socket,id=lan,connect=127.0.0.1:16377", "-device", "virtio-net-pci,netdev=lan,mac=52:54:00:77:01:01", "-display", "none", "-monitor", "none", "-serial", "unix:" + filepath.Join(home, "run/go-lab.sock") + ",server=on,wait=off"}
	log, e := os.OpenFile(filepath.Join(home, "logs/go-lab.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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
	return os.WriteFile(filepath.Join(home, "run/lab.pid"), []byte(strconv.Itoa(c.Process.Pid)), 0600)
}
