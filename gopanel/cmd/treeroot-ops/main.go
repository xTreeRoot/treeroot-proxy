// treeroot-ops supplies the Go-only release path for the existing Go panel.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var assets = []string{"index.html", "app.js", "style.css", "egress.js", "egress.css", "echarts.min.js", "subscription-template.json"}
var connectors = []string{"connector.py", "connector_version.py", "egress_proxy.py"}
var legacy = []string{"app.py", "backup.py", "requirements.txt", "requirements.lock", "egress.py", "limits.py"}
var units = []string{"xray.service", "caddy.service", "treeroot-panel.service", "treeroot-backup.service", "treeroot-backup.timer", "treeroot-limits.service"}

const appDir = "/opt/treeroot"
const dataDir = "/var/lib/treeroot"
const backupDir = "/var/backups/treeroot"
const opsDir = "/root/treeroot-ops"

func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func run(dir string, env []string, name string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	if env != nil {
		c.Env = env
	}
	out, e := c.CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("%s failed: %s", filepath.Base(name), out)
	}
	return out, nil
}
func copyFile(source, target string, mode os.FileMode) error {
	f, e := os.Open(source)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(target), ".ops-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if e = tmp.Chmod(mode); e == nil {
		_, e = io.Copy(tmp, f)
	}
	if e == nil {
		e = tmp.Sync()
	}
	if ce := tmp.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp.Name(), target)
}
func writeJSON(path string, value any) error {
	raw, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}
func openDB(path, mode string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(30000)"}
	db, e := sql.Open("sqlite", uri.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	return db, db.Ping()
}
func snapshotDB(source, target string) error {
	db, e := openDB(source, "ro")
	if e != nil {
		return e
	}
	defer db.Close()
	if _, e = db.Exec("VACUUM INTO ?", target); e != nil {
		return e
	}
	if e = os.Chmod(target, 0600); e != nil {
		return e
	}
	return checkDB(target)
}
func checkDB(path string) error {
	db, e := openDB(path, "ro")
	if e != nil {
		return e
	}
	defer db.Close()
	var result string
	if e = db.QueryRow("PRAGMA integrity_check").Scan(&result); e != nil {
		return e
	}
	if result != "ok" {
		return errors.New("SQLite integrity failed")
	}
	return nil
}
func packFiles(path string, files map[string]string) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := files[name]
		info, e := os.Lstat(p)
		if e != nil || !info.Mode().IsRegular() {
			tw.Close()
			gz.Close()
			f.Close()
			return fmt.Errorf("non-regular package file: %s", name)
		}
		h := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}
		if e = tw.WriteHeader(h); e == nil {
			var input *os.File
			input, e = os.Open(p)
			if e == nil {
				_, e = io.Copy(tw, input)
				input.Close()
			}
		}
		if e != nil {
			return e
		}
	}
	if e = tw.Close(); e == nil {
		e = gz.Close()
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	return e
}
func unpack(path, target string, allowed map[string]bool) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if !allowed[h.Name] || seen[h.Name] || h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != h.Name || h.Size < 0 || h.Size > 100<<20 {
			return errors.New("unsafe or unexpected release member")
		}
		seen[h.Name] = true
		out, e := os.OpenFile(filepath.Join(target, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, tr, h.Size)
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	if len(seen) != len(allowed) {
		return errors.New("incomplete release package")
	}
	return nil
}
func rootOnly() error {
	if os.Geteuid() != 0 || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("requires the verified Linux amd64 root SSH account")
	}
	return nil
}
func httpCheck(origin, cookie string) error {
	client := &http.Client{Timeout: 6 * time.Second}
	for _, item := range [][2]string{{"/api/me", "json"}, {"/assets/app.js", "javascript"}, {"/assets/style.css", "text/css"}, {"/assets/echarts.min.js", "javascript"}} {
		req, _ := http.NewRequest("GET", origin+item[0], nil)
		res, e := client.Do(req)
		if e != nil {
			return e
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), item[1]) {
			return fmt.Errorf("HTTP/MIME failed: %s", item[0])
		}
	}
	if cookie != "" {
		for _, path := range []string{"me", "users", "users?deleted=true", "server", "overview", "activity", "egress", "egress/overview", "home"} {
			req, _ := http.NewRequest("GET", origin+"/api/"+path, nil)
			req.Header.Set("Cookie", "__Host-treeroot="+cookie)
			res, e := client.Do(req)
			if e != nil {
				return e
			}
			var value any
			e = json.NewDecoder(res.Body).Decode(&value)
			res.Body.Close()
			if e != nil || res.StatusCode != 200 {
				return fmt.Errorf("authenticated candidate check failed: %s", path)
			}
		}
	}
	return nil
}

func main() {
	repo := flag.String("root", "", "absolute repository path for local operations")
	archive := flag.String("package", "", "candidate package for root-side update")
	dry := flag.Bool("dry-run", false, "validate without publishing")
	flag.Parse()
	var result any
	var e error
	switch flag.Arg(0) {
	case "ship":
		result, e = ship(*repo, *dry)
	case "status":
		if e = rootOnly(); e == nil {
			e = httpCheck("http://127.0.0.1:19991", "")
		}
		if e == nil {
			e = checkDB(filepath.Join(dataDir, "panel.sqlite3"))
		}
		result = map[string]any{"sqlite": "ok", "http": "ok"}
	case "backup":
		if e = rootOnly(); e == nil {
			result, e = backup()
		}
	case "maintenance-session":
		if e = rootOnly(); e == nil {
			e = maintenanceSession()
		}
		result = map[string]any{"private_file": opsDir + "/home-maintenance-session.json"}
	case "update":
		if e = rootOnly(); e == nil {
			result, e = update(*archive, *dry)
		}
	default:
		e = errors.New("choose ship, status, backup, or update")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
