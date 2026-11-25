package homegateway

import (
	"bufio"
	"context"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

func (a *Agent) discover(ctx context.Context) []Device {
	old := map[string]Device{}
	leased := map[string]string{}
	for _, d := range a.State.Devices {
		d.Online = false
		old[d.MAC] = d
	}
	data, _ := os.ReadFile("/tmp/dhcp.leases")
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	for scan.Scan() {
		parts := strings.Fields(scan.Text())
		if len(parts) < 4 {
			continue
		}
		m, e := MAC(parts[1])
		if e != nil {
			continue
		}
		ip, e := netip.ParseAddr(parts[2])
		if e != nil || !ip.Is4() || !netip.MustParsePrefix("192.168.77.0/24").Contains(ip) {
			continue
		}
		d := old[m]
		d.MAC = m
		d.IP = ip.String()
		d.Name = parts[3]
		d.LeaseExpires, _ = strconv.ParseInt(parts[0], 10, 64)
		if d.LeaseExpires == 0 || d.LeaseExpires > time.Now().Unix() {
			leased[m] = d.IP
		}
		old[m] = d
	}
	neighbors, _ := run(ctx, "ip", "neigh", "show", "dev", a.Config.LAN)
	for _, line := range strings.Split(string(neighbors), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		ip, e := netip.ParseAddr(parts[0])
		if e != nil || !ip.Is4() || !netip.MustParsePrefix("192.168.77.0/24").Contains(ip) {
			continue
		}
		for i, v := range parts {
			if v == "lladdr" && i+1 < len(parts) {
				m, e := MAC(parts[i+1])
				if e != nil {
					continue
				}
				if expected := leased[m]; expected != "" && expected != ip.String() {
					continue
				}
				d := old[m]
				d.MAC = m
				d.IP = ip.String()
				state := parts[len(parts)-1]
				if state == "REACHABLE" || state == "DELAY" || state == "PROBE" {
					d.LastSeen = time.Now().Unix()
				}
				d.Online = state != "FAILED" && d.LastSeen > time.Now().Unix()-60
				old[m] = d
			}
		}
	}
	// A DHCP address can be reassigned. Retaining the previous owner's IP would
	// incorrectly apply its device rules to the new owner.
	owners := map[string]string{}
	for mac, ip := range leased {
		owners[ip] = mac
	}
	for mac, d := range old {
		if owner := owners[d.IP]; owner != "" && owner != mac {
			d.IP = ""
			d.Online = false
			old[mac] = d
		}
	}
	out := make([]Device, 0, len(old))
	for _, d := range old {
		d.LastSeen = max(d.LastSeen, a.ledger.Meters[d.MAC].LastActive)
		d.Online = d.IP != "" && d.LastSeen > time.Now().Unix()-60
		out = append(out, d)
	}
	return out
}
func (a *Agent) ApplyLocal(ctx context.Context, c Config) error {
	return a.apply(ctx, c, bindPolicies(c, a.discover(ctx)))
}

func bindPolicies(c Config, observed []Device) []Device {
	devices := append([]Device(nil), observed...)
	owners := map[string]string{}
	for _, d := range devices {
		if d.IP != "" {
			owners[d.IP] = d.MAC
		}
	}
	for _, p := range c.Devices {
		found := false
		for _, d := range devices {
			if d.MAC == p.MAC {
				found = d.IP != ""
				break
			}
		}
		// A reservation takes effect after DHCP renewal. Until then enforce the
		// device's observed address, and never claim an address used by a peer.
		if !found && p.FixedIP != "" && owners[p.FixedIP] == "" {
			for i := range devices {
				if devices[i].MAC == p.MAC {
					devices = append(devices[:i], devices[i+1:]...)
					break
				}
			}
			devices = append(devices, Device{MAC: p.MAC, IP: p.FixedIP})
		}
	}
	return devices
}
