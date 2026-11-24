package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	BaseURL    string `json:"base_url"`
	DataDir    string `json:"data_dir"`
	XrayConfig string `json:"xray_config"`
	Xray       string `json:"xray_binary"`
	XrayAPI    string `json:"xray_api"`
	AccessLog  string `json:"access_log"`
	WSPath     string `json:"ws_path"`
	EgressDir  string `json:"egress_dir"`
	SSHHost    string `json:"egress_ssh_host"`
	KnownHosts string `json:"egress_known_hosts"`
}

type panel struct {
	config                 config
	database               *sql.DB
	baseURL, root, version string
	queue                  chan writeJob
	queueMu                sync.RWMutex
	closing                bool
	reportSlots, hashSlots chan struct{}
	mux                    *http.ServeMux
	egressMu               sync.Mutex
	live                   map[string]liveSample
	collectorMu            sync.Mutex
	proxyStats             proxyStats
	systemMu               sync.Mutex
	systemAt               time.Time
	system                 row
	cpu                    [2]uint64
	resourceAt             time.Time
	resources              row
}

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip, parseErr := netip.ParseAddr(host)
	return err == nil && parseErr == nil && ip.Unmap().IsLoopback()
}

func readConfig(path string) (config, error) {
	cfg := config{DataDir: "/var/lib/treeroot", XrayConfig: "/usr/local/etc/xray/config.json", Xray: "/usr/local/bin/xray", XrayAPI: "127.0.0.1:10085", AccessLog: "/var/log/treeroot/access.log"}
	content, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(content, &cfg); err != nil {
		return cfg, errors.New("invalid TreeRoot config JSON")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL == "" || !filepath.IsAbs(cfg.DataDir) {
		return cfg, errors.New("config must specify base_url and absolute data_dir")
	}
	if cfg.EgressDir == "" {
		cfg.EgressDir = filepath.Join(cfg.DataDir, "egress")
	}
	return cfg, nil
}

func openDB(cfg config, mode string, connections int) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: filepath.Join(cfg.DataDir, "panel.sqlite3"), RawQuery: "mode=" + mode + "&_pragma=busy_timeout(1000)&_pragma=foreign_keys(1)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func main() {
	configPath := os.Getenv("TREEROOT_CONFIG")
	if configPath == "" {
		configPath = "/etc/treeroot/config.json"
	}
	listen := flag.String("listen", "127.0.0.1:19991", "panel address")
	limitListen := flag.String("limits", "127.0.0.1:10002", "SOCKS listener; empty disables")
	root := flag.String("root", ".", "static files and connector bundle")
	collect := flag.Bool("collect", true, "collect Xray counters and logs")
	backup := flag.Bool("backup", false, "write daily SQLite backup")
	finalPoll := flag.Bool("final-poll", false, "collect once with panel stopped")
	flag.Parse()
	cfg, err := readConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *backup {
		if err := dailyBackup(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}
	if !loopback(*listen) || *limitListen != "" && !loopback(*limitListen) {
		log.Fatal("listeners must use loopback IP addresses")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app, closePanel, err := newPanel(cfg, *root)
	if err != nil {
		log.Fatal(err)
	}
	defer closePanel()
	if *finalPoll {
		if err := app.collectOnce(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := app.publishRegistry(); err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	var workers sync.WaitGroup
	if *limitListen != "" {
		gateway, err := app.startLimits(ctx, *limitListen)
		if err != nil {
			log.Fatal(err)
		}
		defer gateway.Close()
	}
	if *collect {
		workers.Go(func() { app.collect(ctx) })
	}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	workers.Go(func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if server.Shutdown(shutdown) != nil {
			server.Close()
		}
	})
	log.Print("TreeRoot listening on ", listener.Addr())
	fmt.Println(listener.Addr())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}
	cancel()
	workers.Wait()
}
