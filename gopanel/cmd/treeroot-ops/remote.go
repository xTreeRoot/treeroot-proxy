package main

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type backupInfo struct {
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
	Stopped bool   `json:"stopped"`
}

// Root SSH maintenance reuses the panel's ordinary short-lived session format.
// The credential is written only to a root-private file, never command output.
func maintenanceSession() error {
	var random [32]byte
	if _, e := rand.Read(random[:]); e != nil {
		return e
	}
	token := hex.EncodeToString(random[:])
	csrf := hash([]byte(token + "csrf"))
	expires := time.Now().Add(30 * time.Minute).Unix()
	db, e := openDB(filepath.Join(dataDir, "panel.sqlite3"), "rw")
	if e != nil {
		return e
	}
	defer db.Close()
	if _, e = db.Exec("INSERT INTO sessions VALUES(?,?,?)", hash([]byte(token)), csrf, expires); e != nil {
		return e
	}
	return writeJSON(filepath.Join(opsDir, "home-maintenance-session.json"), map[string]any{"token": token, "csrf": csrf, "expires": expires})
}

func backup() (backupInfo, error) {
	var result backupInfo
	if e := os.MkdirAll(backupDir, 0700); e != nil {
		return result, e
	}
	dir, e := os.MkdirTemp(backupDir, ".go-backup-")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(dir)
	if e = os.Mkdir(filepath.Join(dir, "data"), 0700); e != nil {
		return result, e
	}
	db, e := openDB(filepath.Join(dataDir, "panel.sqlite3"), "rw")
	if e != nil {
		return result, e
	}
	defer db.Close()
	if _, e = db.Exec("BEGIN IMMEDIATE"); e != nil {
		return result, e
	}
	defer db.Exec("ROLLBACK")
	if e = snapshotDB(filepath.Join(dataDir, "panel.sqlite3"), filepath.Join(dir, "data/panel.sqlite3")); e != nil {
		return result, e
	}
	sources := map[string]string{
		"/etc/treeroot/config.json": "config/panel.json", "/usr/local/etc/xray/config.json": "config/xray.json", "/etc/caddy/Caddyfile": "config/Caddyfile",
		"/etc/logrotate.d/treeroot": "logrotate/treeroot", "/etc/ssh/sshd_config.d/40-treeroot-egress.conf": "egress/sshd.conf",
		"/etc/systemd/system/treeroot-panel.service.d/egress.conf": "egress/panel-service.conf", "/usr/local/libexec/treeroot-egress-authorize": "egress/authorize.py",
		"/var/lib/treeroot-egress/registry.json": "egress/registry.json", "/etc/systemd/system/treeroot-bbrx.service": "network/bbrx.service",
		"/root/treeroot-ops/bbrx.py": "network/bbrx.py", "/root/treeroot-ops/bbrx.json": "network/bbrx.json", "/root/treeroot-ops/bbrx/installed.json": "network/installed.json",
	}
	for _, name := range units {
		sources["/etc/systemd/system/"+name] = "services/" + name
	}
	for source, target := range sources {
		if _, e = os.Stat(source); os.IsNotExist(e) {
			continue
		}
		if e = copyFile(source, filepath.Join(dir, target), 0600); e != nil {
			return result, e
		}
	}
	if _, e = db.Exec("COMMIT"); e != nil {
		return result, e
	}
	for _, name := range append(append(append([]string{"treeroot-go"}, assets...), connectors...), legacy...) {
		source := filepath.Join(appDir, name)
		if _, e = os.Stat(source); os.IsNotExist(e) {
			continue
		}
		if e = copyFile(source, filepath.Join(dir, "app", name), 0600); e != nil {
			return result, e
		}
	}
	e = filepath.WalkDir("/var/lib/caddy", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "locks" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected Caddy state symlink")
		}
		rel, _ := filepath.Rel("/var/lib/caddy", path)
		return copyFile(path, filepath.Join(dir, "caddy", rel), 0600)
	})
	if e != nil {
		return result, e
	}
	files := map[string]string{}
	hashes := map[string]string{}
	e = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files[rel] = path
		sum, e := digest(path)
		hashes[rel] = sum
		return e
	})
	if e != nil {
		return result, e
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000Z")
	xray, e := run("", nil, "/usr/local/bin/xray", "version")
	if e != nil {
		return result, e
	}
	caddy, e := run("", nil, "/usr/local/bin/caddy", "version")
	if e != nil {
		return result, e
	}
	manifest := map[string]any{"format": 1, "created_at_utc": stamp, "public_ip": "186.244.200.45", "domain": "proxy.treeroot.top", "stopped_for_migration": false, "xray": strings.Split(string(xray), "\n")[0], "caddy": strings.TrimSpace(string(caddy)), "sha256": hashes}
	if e = writeJSON(filepath.Join(dir, "manifest.json"), manifest); e != nil {
		return result, e
	}
	files["manifest.json"] = filepath.Join(dir, "manifest.json")
	result.Archive = filepath.Join(backupDir, "treeroot-"+stamp+".tar.gz")
	if e = packFiles(result.Archive, files); e != nil {
		return result, e
	}
	result.SHA256, e = digest(result.Archive)
	return result, e
}

func tableDigest(db *sql.DB, statement string) (string, error) {
	rows, e := db.Query(statement)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return "", e
	}
	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		refs := make([]any, len(cols))
		for i := range refs {
			refs[i] = &values[i]
		}
		if e = rows.Scan(refs...); e != nil {
			return "", e
		}
		result = append(result, values)
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return "", e
	}
	return hash(raw), rows.Err()
}

var identityQueries = []string{
	"SELECT id,name,client_uuid,email,token,enabled,created FROM users ORDER BY id",
	"SELECT id,username,password_hash FROM admin ORDER BY id",
	"SELECT user_id,deleted_at FROM user_archives ORDER BY user_id",
	"SELECT id,name,port,enabled,deleted,created,browser,access_code,proxy_password,pair_hash,pair_expires,public_key,agent_hash FROM egress_devices ORDER BY id",
}

func identities(path string) ([]string, error) {
	db, e := openDB(path, "ro")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	out := []string{}
	for _, q := range identityQueries {
		s, e := tableDigest(db, q)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}
func usageTotals(path string) (map[string]int64, error) {
	db, e := openDB(path, "ro")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	out := map[string]int64{}
	for _, item := range []struct{ prefix, query string }{{"user:", "SELECT id,upload+download FROM users"}, {"egress:", "SELECT device_id,upload+download FROM egress_totals"}} {
		rows, e := db.Query(item.query)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id string
			var bytes int64
			if e = rows.Scan(&id, &bytes); e != nil {
				rows.Close()
				return nil, e
			}
			out[fmt.Sprint(item.prefix, id)] = bytes
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func completeFingerprint(path string) (map[string]string, error) {
	db, e := openDB(path, "ro")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	result := map[string]string{}
	tables := []string{"users", "admin", "sessions", "user_archives", "domains", "traffic", "egress_devices", "egress_totals", "limit_usage", "limit_daily", "user_limits", "counters", "state"}
	rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name GLOB 'home_*' ORDER BY name")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			return nil, e
		}
		tables = append(tables, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, table := range tables {
		sum, e := tableDigest(db, "SELECT * FROM "+table+" ORDER BY rowid")
		if e != nil {
			return nil, e
		}
		result[table] = sum
	}
	return result, nil
}
func candidate(stage string) error {
	dir := filepath.Join(stage, "test-data")
	if e := os.Mkdir(dir, 0700); e != nil {
		return e
	}
	path := filepath.Join(dir, "panel.sqlite3")
	if e := snapshotDB(filepath.Join(dataDir, "panel.sqlite3"), path); e != nil {
		return e
	}
	var random [32]byte
	if _, e := rand.Read(random[:]); e != nil {
		return e
	}
	token := hex.EncodeToString(random[:])
	csrf := hash([]byte(token + "csrf"))
	db, e := openDB(path, "rw")
	if e != nil {
		return e
	}
	_, e = db.Exec("INSERT INTO sessions VALUES(?,?,?)", hash([]byte(token)), csrf, time.Now().Add(5*time.Minute).Unix())
	db.Close()
	if e != nil {
		return e
	}
	before, e := completeFingerprint(path)
	if e != nil {
		return e
	}
	raw, e := os.ReadFile("/etc/treeroot/config.json")
	if e != nil {
		return e
	}
	var cfg map[string]any
	if e = json.Unmarshal(raw, &cfg); e != nil {
		return e
	}
	cfg["data_dir"], cfg["egress_dir"] = dir, filepath.Join(dir, "egress")
	configPath := filepath.Join(dir, "config.json")
	if e = writeJSON(configPath, cfg); e != nil {
		return e
	}
	if _, e = run("", nil, "chown", "-R", "treeroot:treeroot", dir); e != nil {
		return e
	}
	c := exec.Command("runuser", "-u", "treeroot", "--", "env", "TREEROOT_CONFIG="+configPath, filepath.Join(stage, "treeroot-go"), "-root", stage, "-listen", "127.0.0.1:0", "-limits=", "-collect=false")
	stdout, e := c.StdoutPipe()
	if e != nil {
		return e
	}
	log, e := os.OpenFile(filepath.Join(dir, "candidate.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	c.Stderr = log
	if e = c.Start(); e != nil {
		return e
	}
	defer func() { c.Process.Signal(syscall.SIGTERM); c.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(25 * time.Second):
		return errors.New("candidate startup timeout")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		return errors.New("candidate did not bind loopback")
	}
	if e = httpCheck("http://"+addr, token); e != nil {
		return e
	}
	if _, e = run(stage, nil, "runuser", "-u", "treeroot", "--", "env", "TREEROOT_CONFIG="+configPath, filepath.Join(stage, "treeroot-go"), "-backup"); e != nil {
		return e
	}
	after, e := completeFingerprint(path)
	if e != nil {
		return e
	}
	for table, sum := range before {
		if sum != after[table] {
			return errors.New("candidate changed existing identities, sessions, statistics, or ledgers")
		}
	}
	return checkDB(path)
}

func update(archive string, dry bool) (result map[string]any, err error) {
	if !filepath.IsAbs(archive) {
		return nil, errors.New("absolute package required")
	}
	if err = os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(backupDir, ".go-release.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("another release is running")
	}
	stage, err := os.MkdirTemp("/tmp", "treeroot-go-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	allow := map[string]bool{"treeroot-go": true}
	for _, name := range append(append([]string{}, assets...), connectors...) {
		allow[name] = true
	}
	if err = unpack(archive, stage, allow); err != nil {
		return nil, err
	}
	if err = os.Chmod(stage, 0755); err != nil {
		return nil, err
	}
	for name := range allow {
		mode := os.FileMode(0644)
		if name == "treeroot-go" {
			mode = 0755
		}
		if err = os.Chmod(filepath.Join(stage, name), mode); err != nil {
			return nil, err
		}
	}
	for _, name := range connectors {
		a, e := digest(filepath.Join(stage, name))
		if e != nil {
			return nil, e
		}
		b, e := digest(filepath.Join(appDir, name))
		if e != nil || a != b {
			return nil, errors.New("connector source differs; release rejected")
		}
	}
	unit, err := os.ReadFile("/etc/systemd/system/treeroot-panel.service")
	if err != nil || !strings.Contains(string(unit), "ExecStart=/opt/treeroot/treeroot-go") {
		return nil, errors.New("existing Go service architecture differs")
	}
	if err = candidate(stage); err != nil {
		return nil, err
	}
	if dry {
		return map[string]any{"candidate": "passed", "published": false, "existing_tables_unchanged": true}, nil
	}
	info, err := backup()
	if err != nil {
		return nil, err
	}
	before, err := identities(filepath.Join(dataDir, "panel.sqlite3"))
	if err != nil {
		return nil, err
	}
	usageBefore, err := usageTotals(filepath.Join(dataDir, "panel.sqlite3"))
	if err != nil {
		return nil, err
	}
	saved := strings.TrimSuffix(info.Archive, ".tar.gz") + "-rollback"
	if err = os.Mkdir(saved, 0700); err != nil {
		return nil, err
	}
	files := append([]string{"treeroot-go"}, assets...)
	for _, name := range files {
		if err = copyFile(filepath.Join(appDir, name), filepath.Join(saved, name), 0600); err != nil {
			return nil, err
		}
	}
	if _, err = run("", nil, "systemctl", "stop", "treeroot-panel"); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			return
		}
		run("", nil, "systemctl", "stop", "treeroot-panel")
		for _, name := range files {
			mode := os.FileMode(0644)
			if name == "treeroot-go" {
				mode = 0755
			}
			copyFile(filepath.Join(saved, name), filepath.Join(appDir, name), mode)
		}
		_, restartErr := run("", nil, "systemctl", "start", "treeroot-panel")
		if restartErr != nil {
			err = fmt.Errorf("%w; rollback restart failed", err)
		}
	}()
	for _, name := range files {
		mode := os.FileMode(0644)
		if name == "treeroot-go" {
			mode = 0755
		}
		if err = copyFile(filepath.Join(stage, name), filepath.Join(appDir, name), mode); err != nil {
			return nil, err
		}
	}
	if _, err = run("", nil, "systemctl", "start", "treeroot-panel"); err != nil {
		return nil, err
	}
	for range 30 {
		err = httpCheck("http://127.0.0.1:19991", "")
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	if err = checkDB(filepath.Join(dataDir, "panel.sqlite3")); err != nil {
		return nil, err
	}
	after, err := identities(filepath.Join(dataDir, "panel.sqlite3"))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("production identity preservation check failed")
	}
	usageAfter, err := usageTotals(filepath.Join(dataDir, "panel.sqlite3"))
	if err != nil {
		return nil, err
	}
	for key, value := range usageBefore {
		if next, ok := usageAfter[key]; !ok || next < value {
			return nil, errors.New("production cumulative usage decreased")
		}
	}
	for _, service := range []string{"treeroot-panel", "xray", "caddy"} {
		if _, err = run("", nil, "systemctl", "is-active", "--quiet", service); err != nil {
			return nil, err
		}
	}
	return map[string]any{"published": true, "backup": info, "rollback_files": saved, "identities_preserved": true, "connectors_preserved": true, "sqlite": "ok"}, nil
}
