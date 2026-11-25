package main

import "path/filepath"

func wireEndpointArgs(directory string) []string {
	return []string{"-netdev", "stream,id=physicalwire,addr.type=unix,addr.path=" + filepath.Join(directory, "run/ethernet.sock") + ",server=on",
		"-netdev", "hubport,id=physicalhub,hubid=77,netdev=physicalwire"}
}

func relayArgs(directory string) []string {
	return []string{"-name", "home-ethernet-relay", "-machine", "none", "-nodefaults", "-display", "none", "-monitor", "none", "-serial", "none",
		"-netdev", "stream,id=wire,addr.type=unix,addr.path=" + filepath.Join(directory, "run/ethernet.sock") + ",server=off,reconnect-ms=1000",
		"-netdev", "vmnet-bridged,id=physical,ifname=en0",
		"-netdev", "hubport,id=wirehub,hubid=78,netdev=wire",
		"-netdev", "hubport,id=physicalhub,hubid=78,netdev=physical"}
}
