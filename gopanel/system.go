package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func readText(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func splitFields(text, separator string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if key, value, ok := strings.Cut(line, separator); ok {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return result
}
func firstField(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
func uintValue(text string) uint64 { value, _ := strconv.ParseUint(text, 10, 64); return value }
func usageInfo(total, used uint64) row {
	return row{"total": total, "used": used, "percent": float64(used) * 100 / float64(total)}
}

func (app *panel) resourceInfo(_ http.ResponseWriter, _ *http.Request) (any, error) {
	app.systemMu.Lock()
	defer app.systemMu.Unlock()
	now := time.Now()
	if now.Sub(app.resourceAt) < 4*time.Second {
		return app.resources, nil
	}
	result := row{"sampled_at": now.Unix(), "cpu_percent": nil, "cpu_cores": runtime.NumCPU(), "load_average": nil, "memory": nil, "disk": nil, "uptime_seconds": nil}
	if cpu := strings.Fields(strings.SplitN(readText("/proc/stat"), "\n", 2)[0]); len(cpu) >= 9 {
		var total uint64
		for _, text := range cpu[1:9] {
			total += uintValue(text)
		}
		idle := uintValue(cpu[4]) + uintValue(cpu[5])
		if app.cpu[0] != 0 && total > app.cpu[0] && idle >= app.cpu[1] {
			result["cpu_percent"] = 100 * (1 - min(1, float64(idle-app.cpu[1])/float64(total-app.cpu[0])))
		}
		app.cpu = [2]uint64{total, idle}
	}
	if parts := strings.Fields(readText("/proc/loadavg")); len(parts) >= 3 {
		values := [3]float64{}
		for idx := range values {
			values[idx], _ = strconv.ParseFloat(parts[idx], 64)
		}
		result["load_average"] = values
	}
	values := splitFields(readText("/proc/meminfo"), ":")
	if total := uintValue(firstField(values["MemTotal"])) * 1024; total > 0 {
		result["memory"] = usageInfo(total, total-min(total, uintValue(firstField(values["MemAvailable"]))*1024))
	}
	var disk unix.Statfs_t
	if unix.Statfs(app.config.DataDir, &disk) == nil && disk.Blocks > 0 {
		size := uint64(disk.Bsize)
		result["disk"] = usageInfo(uint64(disk.Blocks)*size, uint64(disk.Blocks-disk.Bfree)*size)
	}
	if uptime, err := strconv.ParseFloat(firstField(readText("/proc/uptime")), 64); err == nil {
		result["uptime_seconds"] = int64(uptime)
	}
	app.resources, app.resourceAt = result, now
	return result, nil
}

func (app *panel) serverInfo(_ http.ResponseWriter, _ *http.Request) (any, error) {
	app.systemMu.Lock()
	defer app.systemMu.Unlock()
	now := time.Now()
	if now.Sub(app.systemAt) < 10*time.Second {
		return app.system, nil
	}
	osName := strings.Trim(splitFields(readText("/etc/os-release"), "=")["PRETTY_NAME"], "\"")
	if osName == "" {
		osName = runtime.GOOS
	}
	hostname, _ := os.Hostname()
	timezone, _ := now.Zone()
	base, _ := url.Parse(app.baseURL)
	kernel := readText("/proc/sys/kernel/osrelease")
	architecture := runtime.GOARCH
	switch architecture {
	case "amd64":
		architecture = "x86_64"
	case "arm64":
		architecture = "aarch64"
	}
	result := row{"sampled_at": now.Unix(), "hostname": hostname, "kernel": kernel, "architecture": architecture, "os": osName,
		"cpu_model": splitFields(readText("/proc/cpuinfo"), ":")["model name"], "domain": base.Host, "protocol": "VMess · WebSocket · TLS",
		"timezone": timezone, "processes": nil, "database_bytes": nil, "versions": row{"go": runtime.Version()}}
	if entries, err := os.ReadDir("/proc"); err == nil {
		count := 0
		for _, item := range entries {
			if _, err := strconv.Atoi(item.Name()); err == nil {
				count++
			}
		}
		result["processes"] = count
	}
	path := filepath.Join(app.config.DataDir, "panel.sqlite3")
	if info, err := os.Stat(path); err == nil {
		size := info.Size()
		if wal, err := os.Stat(path + "-wal"); err == nil {
			size += wal.Size()
		}
		result["database_bytes"] = size
	}
	services := []row{}
	output, err := command(2*time.Second, "systemctl", "show", "xray.service", "caddy.service", "treeroot-panel.service",
		"treeroot-backup.timer", "socialcrawlerhub.service", "socialcrawlerhub-browser.service", "mysql.service",
		"--property=Id,Description,ActiveState,SubState,MainPID,MemoryCurrent,ActiveEnterTimestamp,WorkingDirectory")
	if err == nil {
		for _, block := range strings.Split(strings.TrimSpace(string(output)), "\n\n") {
			info := splitFields(block, "=")
			if info["Id"] == "" {
				continue
			}
			item := row{"name": info["Id"], "description": info["Description"], "state": info["ActiveState"], "detail": info["SubState"],
				"pid": uintValue(info["MainPID"]), "started_at": info["ActiveEnterTimestamp"], "memory": nil, "working_directory": nil}
			if value, err := strconv.ParseUint(info["MemoryCurrent"], 10, 64); err == nil {
				item["memory"] = value
			}
			if value := info["WorkingDirectory"]; value != "" {
				item["working_directory"] = value
			}
			services = append(services, item)
		}
	}
	result["services"] = services
	app.system, app.systemAt = result, now
	return result, nil
}
