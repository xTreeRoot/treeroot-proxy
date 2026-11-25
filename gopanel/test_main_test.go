package main

import (
	"fmt"
	"os"
	"testing"
)

// The test executable also serves as a Go-only Xray CLI fixture. Production
// always invokes the configured vendor binary; no fake path enters the panel.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "run":
			if os.Getenv("TREEROOT_TEST_XRAY_FAIL") == "1" {
				os.Exit(1)
			}
			os.Exit(0)
		case "api":
			if len(os.Args) > 2 {
				switch os.Args[2] {
				case "adu":
					fmt.Println("Added 1 user(s)")
				case "rmu":
					fmt.Println("Removed 1 user(s)")
				case "lso":
					fmt.Println(`{"outbounds":[]}`)
				}
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
