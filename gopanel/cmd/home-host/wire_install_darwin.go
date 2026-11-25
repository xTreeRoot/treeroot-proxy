package main

import (
	"debug/macho"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const wireInstallDir = "/Library/Application Support/TreeRoot Home Wire"
const wireLabel = "top.treeroot.home-wire"

func wirePlist(directory string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s/home-host</string><string>-home</string><string>%s</string><string>wire-service</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>10</integer>
<key>StandardOutPath</key><string>%s/relay.log</string>
<key>StandardErrorPath</key><string>%s/relay-error.log</string>
<key>Umask</key><integer>63</integer>
</dict></plist>
`, wireLabel, xmlText(wireInstallDir), xmlText(directory), xmlText(wireInstallDir), xmlText(wireInstallDir)))
}

// Copy the complete Homebrew dylib closure and rewrite its references before
// signing. A root service must not execute mutable code from a user's Homebrew.
func installRelayBundle(stage, final string) error {
	qemu := "/opt/homebrew/bin/qemu-system-aarch64"
	pending := []string{qemu}
	dependencies := map[string][]string{}
	filenames := map[string]string{}
	for len(pending) > 0 {
		path := pending[0]
		pending = pending[1:]
		if _, ok := dependencies[path]; ok {
			continue
		}
		name := filepath.Base(path)
		if previous, ok := filenames[name]; ok && previous != path {
			a, _ := filepath.EvalSymlinks(previous)
			b, _ := filepath.EvalSymlinks(path)
			if a != b {
				return fmt.Errorf("中继依赖名称冲突：%s", name)
			}
		}
		filenames[name] = path
		f, e := macho.Open(path)
		if e != nil {
			return e
		}
		libs, e := f.ImportedLibraries()
		f.Close()
		if e != nil {
			return e
		}
		dependencies[path] = libs
		for _, lib := range libs {
			if strings.HasPrefix(lib, "/opt/homebrew/") {
				pending = append(pending, lib)
			} else if !strings.HasPrefix(lib, "/usr/lib/") && !strings.HasPrefix(lib, "/System/Library/") {
				return fmt.Errorf("未核实的中继依赖：%s", lib)
			}
		}
	}
	names := make([]string, 0, len(filenames))
	for name := range filenames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if e := copyFile(filenames[name], filepath.Join(stage, name)); e != nil {
			return e
		}
		if e := os.Chmod(filepath.Join(stage, name), 0755); e != nil {
			return e
		}
	}
	for _, name := range names {
		target := filepath.Join(stage, name)
		var args []string
		if name != "qemu-system-aarch64" {
			args = append(args, "-id", filepath.Join(final, name))
		}
		for _, lib := range dependencies[filenames[name]] {
			if strings.HasPrefix(lib, "/opt/homebrew/") {
				args = append(args, "-change", lib, filepath.Join(final, filepath.Base(lib)))
			}
		}
		if len(args) > 0 {
			if out, e := exec.Command("/usr/bin/install_name_tool", append(args, target)...).CombinedOutput(); e != nil {
				return fmt.Errorf("依赖路径改写失败：%s", out)
			}
		}
		if out, e := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", target).CombinedOutput(); e != nil {
			return fmt.Errorf("中继签名失败：%s", out)
		}
		f, e := macho.Open(target)
		if e != nil {
			return e
		}
		libs, e := f.ImportedLibraries()
		f.Close()
		if e != nil {
			return e
		}
		for _, lib := range libs {
			if !strings.HasPrefix(lib, final+"/") && !strings.HasPrefix(lib, "/usr/lib/") && !strings.HasPrefix(lib, "/System/Library/") {
				return errors.New("中继仍依赖可写入的外部程序")
			}
		}
	}
	return os.Mkdir(filepath.Join(stage, "modules"), 0755)
}

func installWireService() error {
	if e := requireRoot(); e != nil {
		return e
	}
	runtime, e := activeRuntime(home)
	if e != nil {
		return e
	}
	home = runtime
	s, e := networkSnapshotNow()
	if e != nil || !fixedEthernetBaseline(s) || !strings.Contains(s.DefaultRoute, "interface: en1") {
		return errors.New("有线固定地址或 Wi-Fi 默认路由不符，未安装中继")
	}
	if e = verifyRunningVM(); e != nil {
		return e
	}
	if !alive(pid()) {
		return errors.New("家庭网关尚未启动")
	}
	// Refuse a pre-existing user-owned/symlink installation directory.
	if info, e := os.Lstat(wireInstallDir); e == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return errors.New("中继安装目录权限异常")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	stage, e := os.MkdirTemp(filepath.Dir(wireInstallDir), ".treeroot-wire-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	if e = installRelayBundle(stage, wireInstallDir); e != nil {
		return e
	}
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	if e = copyFile(binary, filepath.Join(stage, "home-host")); e != nil {
		return e
	}
	if e = os.Chmod(filepath.Join(stage, "home-host"), 0755); e != nil {
		return e
	}
	if e = os.Chmod(stage, 0755); e != nil {
		return e
	}
	plist := "/Library/LaunchDaemons/" + wireLabel + ".plist"
	if e = os.WriteFile(filepath.Join(stage, "service.plist"), wirePlist(home), 0600); e != nil {
		return e
	}
	if e = exec.Command("/usr/bin/plutil", "-lint", filepath.Join(stage, "service.plist")).Run(); e != nil {
		return e
	}
	exec.Command("/bin/launchctl", "bootout", "system/"+wireLabel).Run()
	old := wireInstallDir + ".previous"
	if _, e = os.Lstat(old); e == nil {
		return errors.New("存在上次中继备份，需先核实")
	}
	if _, e = os.Stat(wireInstallDir); e == nil {
		if e = os.Rename(wireInstallDir, old); e != nil {
			return e
		}
	}
	if e = os.Rename(stage, wireInstallDir); e != nil {
		os.Rename(old, wireInstallDir)
		return e
	}
	if e = os.WriteFile(plist, wirePlist(home), 0600); e != nil {
		return e
	}
	if out, e := exec.Command("/bin/launchctl", "bootstrap", "system", plist).CombinedOutput(); e != nil {
		return fmt.Errorf("中继服务注册失败：%s", out)
	}
	fmt.Println("Root-owned Ethernet relay installed; user VM and Wi-Fi route preserved")
	return nil
}
