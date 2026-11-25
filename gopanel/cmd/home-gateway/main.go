package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"treeroot/gopanel/homeap"
	hg "treeroot/gopanel/homegateway"
)

func main() {
	path := flag.String("config", "/etc/treeroot-home/agent.json", "private agent configuration")
	desired := flag.String("desired", "", "local structured configuration (isolated test only)")
	transaction := flag.String("transaction", "", "local recovery journal identifier")
	pairFile := flag.String("pair-file", "", "private file containing a one-time pairing code")
	wifiFile := flag.String("wifi-file", "", "private wireless settings file")
	flag.Parse()
	a, e := hg.ReadAgent(*path)
	if e != nil {
		fail(e)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch flag.Arg(0) {
	case "ap-status":
		view, err := a.APStatus(ctx)
		if err != nil {
			fail(err)
		}
		json.NewEncoder(os.Stdout).Encode(view)
	case "ap-set":
		if *wifiFile == "" {
			fail(fmt.Errorf("private wireless settings file required"))
		}
		raw, err := os.ReadFile(*wifiFile)
		if err != nil {
			fail(err)
		}
		var settings homeap.Settings
		if json.Unmarshal(raw, &settings) != nil {
			fail(fmt.Errorf("invalid wireless settings"))
		}
		if e = a.SetWiFi(ctx, settings, false); e != nil {
			fail(e)
		}
		fmt.Println("Wi-Fi configuration confirmed")
	case "ap-recover-watch":
		if e = a.RecoverAP(ctx, *transaction, true); e != nil {
			fail(e)
		}
	case "observe-lab":
		if a.Config.Mode != "isolated" {
			fail(fmt.Errorf("isolated mode required"))
		}
		events, err := a.ObserveLocal(ctx, 30*time.Second)
		if err != nil {
			fail(err)
		}
		json.NewEncoder(os.Stdout).Encode(events)
	case "recover-watch":
		if e = a.Recover(ctx, *transaction, true); e != nil {
			fail(e)
		}
	case "recover":
		if e = a.Recover(ctx, "", false); e != nil {
			fail(e)
		}
	case "inspect":
		json.NewEncoder(os.Stdout).Encode(a.Diagnostics(ctx))
	case "apply-lab":
		if a.Config.Mode != "isolated" || *desired == "" {
			fail(fmt.Errorf("explicit isolated configuration required"))
		}
		lock, err := a.Lock()
		if err != nil {
			fail(err)
		}
		defer lock.Close()
		data, e := os.ReadFile(*desired)
		if e != nil {
			fail(e)
		}
		var c hg.Config
		if e = json.Unmarshal(data, &c); e != nil {
			fail(e)
		}
		if e = a.ApplyLocal(ctx, c); e != nil {
			fail(e)
		}
		fmt.Println("Isolated configuration applied")
	case "run":
		if e = a.Run(ctx); e != nil {
			fail(e)
		}
	case "pair":
		if *pairFile == "" {
			fail(fmt.Errorf("private pairing file required"))
		}
		code, err := os.ReadFile(*pairFile)
		if err != nil {
			fail(err)
		}
		if e = a.Pair(ctx, strings.TrimSpace(string(code))); e != nil {
			fail(e)
		}
		os.Remove(*pairFile)
		fmt.Println("Gateway paired")
	default:
		fail(fmt.Errorf("choose inspect, apply-lab or run"))
	}
}
func fail(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
