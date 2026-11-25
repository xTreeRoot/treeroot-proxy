package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type target struct {
	Host   string `json:"host"`
	User   string `json:"ssh_user"`
	Port   int    `json:"ssh_port"`
	Domain string `json:"domain"`
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func sshOptions(root string) []string {
	return []string{"-i", filepath.Join(root, "private/ssh/id_ed25519"), "-o", "IdentitiesOnly=yes", "-o", "UserKnownHostsFile=" + filepath.Join(root, "private/ssh/known_hosts"), "-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
}
func ssh(root string, t target, command string) ([]byte, error) {
	args := append(sshOptions(root), "-p", strconv.Itoa(t.Port), t.User+"@"+t.Host, command)
	return run(root, nil, "ssh", args...)
}
func scp(root string, t target, source, destination string) error {
	args := append([]string{"-q"}, sshOptions(root)...)
	args = append(args, "-P", strconv.Itoa(t.Port), source, destination)
	_, e := run(root, nil, "scp", args...)
	return e
}
func buildEnv() []string {
	out := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "CGO_ENABLED=") && !strings.HasPrefix(item, "GOOS=") && !strings.HasPrefix(item, "GOARCH=") {
			out = append(out, item)
		}
	}
	return append(out, "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
}
func ship(root string, dry bool) (map[string]any, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute repository -root required")
	}
	branch, e := run(root, nil, "git", "branch", "--show-current")
	if e != nil || strings.TrimSpace(string(branch)) != "main" {
		return nil, errors.New("only main may publish")
	}
	raw, e := os.ReadFile(filepath.Join(root, "deploy/server.json"))
	if e != nil {
		return nil, e
	}
	var t target
	if json.Unmarshal(raw, &t) != nil || t.User != "root" || t.Port != 22 || t.Domain != "proxy.treeroot.top" {
		return nil, errors.New("deployment target differs from verified architecture")
	}
	if _, e = run(filepath.Join(root, "frontend"), nil, "npm", "run", "build"); e != nil {
		return nil, e
	}
	dir, e := os.MkdirTemp(filepath.Join(root, "work"), "go-release-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	for _, item := range []struct{ name, pkg string }{{"treeroot-go", "."}, {"treeroot-ops-go", "./cmd/treeroot-ops"}} {
		if _, e = run(filepath.Join(root, "gopanel"), buildEnv(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dir, item.name), item.pkg); e != nil {
			return nil, e
		}
	}
	files := map[string]string{"treeroot-go": filepath.Join(dir, "treeroot-go")}
	for _, name := range append(append([]string{}, assets...), connectors...) {
		files[name] = filepath.Join(root, name)
	}
	archive := filepath.Join(dir, "release.tar.gz")
	if e = packFiles(archive, files); e != nil {
		return nil, e
	}
	if _, e = ssh(root, t, "install -d -m 700 "+opsDir); e != nil {
		return nil, e
	}
	remote := t.User + "@" + t.Host + ":"
	if e = scp(root, t, filepath.Join(dir, "treeroot-ops-go"), remote+opsDir+"/treeroot-ops-go.next"); e != nil {
		return nil, e
	}
	if _, e = ssh(root, t, "chmod 700 "+opsDir+"/treeroot-ops-go.next && mv "+opsDir+"/treeroot-ops-go.next "+opsDir+"/treeroot-ops-go"); e != nil {
		return nil, e
	}
	remoteArchive := opsDir + "/home-release-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".tar.gz"
	if e = scp(root, t, archive, remote+remoteArchive); e != nil {
		return nil, e
	}
	defer ssh(root, t, "rm -f "+quote(remoteArchive))
	command := opsDir + "/treeroot-ops-go -package " + quote(remoteArchive)
	if dry {
		command += " -dry-run"
	}
	command += " update"
	out, e := ssh(root, t, command)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if json.Unmarshal(out, &result) != nil {
		return nil, errors.New("release completed but metadata was unreadable; inspect server before retrying")
	}
	if info, ok := result["backup"].(map[string]any); ok {
		path, ok := info["archive"].(string)
		if !ok || !strings.HasPrefix(path, backupDir+"/treeroot-") || filepath.Ext(path) != ".gz" {
			return nil, errors.New("unexpected backup path")
		}
		folder := filepath.Join(root, "private/backups")
		if e = os.MkdirAll(folder, 0700); e != nil {
			return nil, e
		}
		local := filepath.Join(folder, filepath.Base(path))
		if e = scp(root, t, remote+path, local); e != nil {
			return nil, fmt.Errorf("published; backup download failed: %w", e)
		}
		if e = os.Chmod(local, 0600); e != nil {
			return nil, e
		}
		sum, e := digest(local)
		if e != nil || sum != info["sha256"] {
			return nil, errors.New("published; downloaded backup checksum mismatch")
		}
		if e = os.WriteFile(local+".sha256", []byte(sum+"  "+filepath.Base(local)+"\n"), 0600); e != nil {
			return nil, e
		}
		if e = verifyBackup(local); e != nil {
			return nil, e
		}
		result["local_backup"] = local
	}
	return result, nil
}
