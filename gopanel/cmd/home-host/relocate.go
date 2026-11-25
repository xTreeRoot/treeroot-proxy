package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func activeRuntime(directory string) (string, error) {
	seen := map[string]bool{}
	for range 5 {
		if seen[directory] {
			return "", errors.New("运行目录指向循环，未操作虚拟机")
		}
		seen[directory] = true
		raw, e := os.ReadFile(filepath.Join(directory, "private/runtime-location.json"))
		if os.IsNotExist(e) {
			return directory, nil
		}
		if e != nil {
			return "", e
		}
		var location struct {
			Directory string `json:"directory"`
			Mode      string `json:"mode"`
		}
		if json.Unmarshal(raw, &location) != nil || !filepath.IsAbs(location.Directory) || location.Mode != "isolated" {
			return "", errors.New("运行目录记录无效，未操作虚拟机")
		}
		directory = location.Directory
	}
	return "", errors.New("运行目录指向过多，未操作虚拟机")
}

// Keep a cold source disk intact. Runtime files live outside Documents so a
// login service does not depend on an interactive Documents permission prompt.
func relocateRuntime(destination string) (err error) {
	if !filepath.IsAbs(destination) || destination == home {
		return errors.New("需要新的绝对路径私密运行目录")
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return errors.New("目标已存在或无法检查，未覆盖")
	}
	mode, _ := os.ReadFile(filepath.Join(home, "run/mode"))
	if strings.TrimSpace(string(mode)) != "isolated" {
		return errors.New("只迁移隔离模式")
	}
	if e := verifyRunningVM(); e != nil {
		return e
	}
	sourceHome := home
	paused := filepath.Join(home, "run/paused")
	if e := os.WriteFile(paused, []byte("runtime migration\n"), 0600); e != nil {
		return e
	}
	if e := stop(); e != nil {
		return e
	}
	// Before the new VM starts, any failed preparation restores the old VM.
	defer func() {
		if err != nil && home == sourceHome {
			os.Remove(paused)
			if e := start(); e != nil {
				err = fmt.Errorf("%w；原网关重启失败：%v", err, e)
			}
		}
	}()
	if e := os.Mkdir(destination, 0700); e != nil {
		return e
	}
	for _, name := range []string{"private", "run", "logs", "bin"} {
		if e := os.Mkdir(filepath.Join(destination, name), 0700); e != nil {
			return e
		}
	}
	for _, name := range []string{"id_ed25519", "known_hosts"} {
		if e := copyFile(filepath.Join(home, "private", name), filepath.Join(destination, "private", name)); e != nil {
			return e
		}
	}
	targetDisk := filepath.Join(destination, "private/gateway.qcow2")
	if e := command("/opt/homebrew/bin/qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", filepath.Join(home, "private/gateway.qcow2"), targetDisk); e != nil {
		return e
	}
	if e := os.Chmod(targetDisk, 0600); e != nil {
		return e
	}
	if e := command("/opt/homebrew/bin/qemu-img", "check", targetDisk); e != nil {
		return e
	}
	if e := os.WriteFile(filepath.Join(destination, "run/mode"), []byte("isolated"), 0600); e != nil {
		return e
	}
	home = destination
	if e := start(); e != nil {
		home = sourceHome
		return e
	}
	if e := privateJSON(filepath.Join(sourceHome, "private/runtime-location.json"), map[string]string{"directory": destination, "mode": "isolated"}); e != nil {
		return e
	}
	fmt.Println("Isolated runtime copied and started; cold source retained, physical network unchanged")
	return nil
}
