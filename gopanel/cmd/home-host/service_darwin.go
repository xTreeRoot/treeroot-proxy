package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const serviceLabel = "top.treeroot.home-gateway"

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func servicePlist(binary, directory string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>-home</string><string>%s</string><string>supervise</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>15</integer>
<key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
<key>Umask</key><integer>63</integer>
<key>AbandonProcessGroup</key><true/>
</dict></plist>
`, serviceLabel, xmlText(binary), xmlText(directory), xmlText(filepath.Join(directory, "logs/supervisor.log")), xmlText(filepath.Join(directory, "logs/supervisor-error.log"))))
}

func installService() error {
	if os.Geteuid() == 0 {
		return errors.New("隔离网关使用当前用户登录服务，无需 sudo")
	}
	if e := verifyRunningVM(); e != nil {
		return e
	}
	mode, _ := os.ReadFile(filepath.Join(home, "run", "mode"))
	if string(mode) != "isolated" {
		return errors.New("只安装已核实的隔离网关服务")
	}
	userDir, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	binDir := filepath.Join(home, "bin")
	if e = os.MkdirAll(binDir, 0700); e != nil {
		return e
	}
	source, e := os.Open(executable)
	if e != nil {
		return e
	}
	defer source.Close()
	target, e := os.CreateTemp(binDir, ".home-host-*")
	if e != nil {
		return e
	}
	defer os.Remove(target.Name())
	if e = target.Chmod(0700); e == nil {
		_, e = io.Copy(target, source)
	}
	if e == nil {
		e = target.Sync()
	}
	if ce := target.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	binary := filepath.Join(binDir, "home-host")
	if e = os.Rename(target.Name(), binary); e != nil {
		return e
	}
	folder := filepath.Join(userDir, "Library/LaunchAgents")
	if e = os.MkdirAll(folder, 0700); e != nil {
		return e
	}
	path := filepath.Join(folder, serviceLabel+".plist")
	if e = os.WriteFile(path, servicePlist(binary, home), 0600); e != nil {
		return e
	}
	if e = exec.Command("/usr/bin/plutil", "-lint", path).Run(); e != nil {
		return errors.New("家庭服务配置校验失败")
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	// This exact label belongs to this project; unrelated agents are untouched.
	exec.Command("/bin/launchctl", "bootout", domain+"/"+serviceLabel).Run()
	// bootout returns before launchd has finished reaping the old job.
	var out []byte
	for attempt := range 5 {
		out, e = exec.Command("/bin/launchctl", "bootstrap", domain, path).CombinedOutput()
		if e == nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	if e != nil {
		return fmt.Errorf("家庭服务注册失败：%s", out)
	}
	fmt.Println("Isolated gateway login supervisor registered; physical Ethernet unchanged")
	return nil
}
