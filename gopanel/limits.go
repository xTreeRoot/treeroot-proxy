package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

type usage struct {
	id           int64
	day          string
	total, daily int64
}
type account struct {
	mu          sync.Mutex
	current     row
	usage       usage
	next        time.Time
	connections map[net.Conn]context.CancelFunc
}
type gateway struct {
	app      *panel
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
	accounts map[string]*account
	ready    time.Time
}

var quotaExceeded = errors.New("quota exceeded or authorization expired")

func (app *panel) startLimits(parent context.Context, address string) (*gateway, error) {
	ctx, cancel := context.WithCancel(parent)
	gateway := &gateway{app: app, ctx: ctx, cancel: cancel, accounts: map[string]*account{}}
	if err := gateway.reload(ctx); err != nil {
		cancel()
		return nil, err
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		cancel()
		return nil, err
	}
	gateway.listener = listener
	gateway.wg.Go(gateway.serve)
	gateway.wg.Go(gateway.maintain)
	return gateway, nil
}

func (gate *gateway) Close() {
	gate.cancel()
	gate.listener.Close()
	gate.wg.Wait()
	logError("flush limiter", gate.flush(context.Background()))
}

func (gate *gateway) reload(ctx context.Context) error {
	day := shanghaiDay()
	rows, err := query(ctx, gate.app.database, `SELECT u.id,u.email,u.client_uuid,u.enabled,`+limitsSelect+` FROM users u`+limitsJoin, day)
	if err != nil {
		return err
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	for _, item := range rows {
		email := item.text("email")
		entry := gate.accounts[email]
		if entry == nil {
			entry = &account{connections: map[net.Conn]context.CancelFunc{}}
			gate.accounts[email] = entry
		}
		entry.mu.Lock()
		old := entry.current
		entry.current = item
		if entry.usage.day != day {
			entry.usage.daily = 0
		}
		entry.usage = usage{item.number("id"), day, max(entry.usage.total, item.number("total_used")), max(entry.usage.daily, item.number("daily_used"))}
		if old.number("speed_bps") != item.number("speed_bps") {
			entry.next = time.Time{}
		}
		if item.number("enabled") != 1 || old.text("client_uuid") != item.text("client_uuid") || entry.blocked() {
			entry.disconnect()
		}
		entry.mu.Unlock()
	}
	gate.ready = time.Now()
	return nil
}

func (entry *account) blocked() bool {
	total, daily := entry.current.number("total_bytes"), entry.current.number("daily_bytes")
	return total > 0 && entry.usage.total >= total || daily > 0 && entry.usage.daily >= daily
}

func (entry *account) disconnect() {
	for connection, cancel := range entry.connections {
		cancel()
		connection.Close()
	}
}

func saveUsage(tx *sql.Tx, current usage) error {
	if _, err := tx.Exec(`INSERT INTO limit_usage VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET total_bytes=excluded.total_bytes WHERE excluded.total_bytes>limit_usage.total_bytes`, current.id, current.total); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO limit_daily VALUES(?,?,?) ON CONFLICT(user_id,day) DO UPDATE SET bytes=excluded.bytes WHERE excluded.bytes>limit_daily.bytes`, current.id, current.day, current.daily)
	return err
}

func (gate *gateway) flush(ctx context.Context) error {
	gate.mu.RLock()
	values := make([]usage, 0, len(gate.accounts))
	for _, entry := range gate.accounts {
		entry.mu.Lock()
		values = append(values, entry.usage)
		entry.mu.Unlock()
	}
	gate.mu.RUnlock()
	return gate.app.write(ctx, func(tx *sql.Tx) error {
		for _, value := range values {
			if err := saveUsage(tx, value); err != nil {
				return err
			}
		}
		return state(tx, "limits_heartbeat", float64(time.Now().UnixMilli())/1000)
	})
}

func (gate *gateway) maintain() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for gate.ctx.Err() == nil {
		err := gate.flush(gate.ctx)
		if err == nil {
			err = gate.reload(gate.ctx)
		}
		if gate.ctx.Err() == nil {
			logError("limiter refresh", err)
		}
		select {
		case <-gate.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (gate *gateway) take(ctx context.Context, entry *account, requested int, packet bool) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		gate.mu.RLock()
		age := time.Since(gate.ready)
		gate.mu.RUnlock()
		entry.mu.Lock()
		if age > 5*time.Second || entry.current.number("enabled") != 1 {
			entry.mu.Unlock()
			return 0, quotaExceeded
		}
		if day := shanghaiDay(); entry.usage.day != day {
			last := entry.usage
			if err := gate.app.write(ctx, func(tx *sql.Tx) error { return saveUsage(tx, last) }); err != nil {
				entry.mu.Unlock()
				return 0, err
			}
			entry.usage.day, entry.usage.daily = day, 0
		}
		if entry.blocked() {
			entry.mu.Unlock()
			return 0, quotaExceeded
		}
		total, daily, speed := entry.current.number("total_bytes"), entry.current.number("daily_bytes"), entry.current.number("speed_bps")
		if speed > 0 && time.Until(entry.next) > 0 {
			delay := min(time.Until(entry.next), 100*time.Millisecond)
			entry.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		count := int64(requested)
		if total > 0 {
			count = min(count, total-entry.usage.total)
		}
		if daily > 0 {
			count = min(count, daily-entry.usage.daily)
		}
		if packet && count < int64(requested) {
			entry.mu.Unlock()
			return 0, quotaExceeded
		}
		if speed > 0 && !packet {
			count = min(count, max(1, speed/80))
		}
		next := entry.usage
		next.total += count
		next.daily += count
		if total > 0 || daily > 0 {
			if err := gate.app.write(ctx, func(tx *sql.Tx) error { return saveUsage(tx, next) }); err != nil {
				entry.mu.Unlock()
				return 0, err
			}
		}
		entry.usage = next
		if speed > 0 {
			entry.next = time.Now().Add(time.Duration(count * 8 * int64(time.Second) / speed))
		}
		entry.mu.Unlock()
		return int(count), nil
	}
}

func (gate *gateway) serve() {
	slots := make(chan struct{}, 1024)
	stop := context.AfterFunc(gate.ctx, func() { gate.listener.Close() })
	defer stop()
	for {
		connection, err := gate.listener.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			gate.wg.Go(func() { defer func() { <-slots }(); gate.handle(connection) })
		default:
			connection.Close()
		}
	}
}

func readN(reader io.Reader, size int) ([]byte, error) {
	buffer := make([]byte, size)
	_, err := io.ReadFull(reader, buffer)
	return buffer, err
}

func socksAddress(reader *bufio.Reader) (string, uint16, error) {
	kind, err := reader.ReadByte()
	if err != nil {
		return "", 0, err
	}
	var size int
	switch kind {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		value, err := reader.ReadByte()
		if err != nil {
			return "", 0, err
		}
		size = int(value)
	default:
		return "", 0, errors.New("invalid SOCKS address")
	}
	data, err := readN(reader, size+2)
	if err != nil {
		return "", 0, err
	}
	host := string(data[:size])
	if kind != 3 {
		ip, _ := netip.AddrFromSlice(data[:size])
		host = ip.String()
	}
	return host, binary.BigEndian.Uint16(data[size:]), nil
}

func socksEndpoint(addr netip.AddrPort) []byte {
	ip := addr.Addr().Unmap()
	kind := byte(4)
	if ip.Is4() {
		kind = 1
	}
	return binary.BigEndian.AppendUint16(append([]byte{kind}, ip.AsSlice()...), addr.Port())
}

var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func resolve(ctx context.Context, host string, port uint16) ([]netip.Addr, error) {
	if port == 0 || host == "" || strings.Contains(host, "%") {
		return nil, errors.New("invalid destination")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !publicIP(address) {
			return nil, errors.New("non-public destination")
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("destination unavailable")
	}
	return addresses, nil
}

func (gate *gateway) handle(connection net.Conn) {
	ctx, cancel := context.WithCancel(gate.ctx)
	defer cancel()
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	connection.SetDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(connection)
	hello, err := readN(reader, 2)
	if err != nil {
		return
	}
	methods, err := readN(reader, int(hello[1]))
	if err != nil {
		return
	}
	if hello[0] != 5 || !bytes.Contains(methods, []byte{2}) {
		connection.Write([]byte{5, 255})
		return
	}
	if _, err := connection.Write([]byte{5, 2}); err != nil {
		return
	}
	head, err := readN(reader, 2)
	if err != nil {
		return
	}
	name, err := readN(reader, int(head[1]))
	if err != nil {
		return
	}
	size, err := reader.ReadByte()
	if err != nil {
		return
	}
	password, err := readN(reader, int(size))
	if err != nil {
		return
	}
	gate.mu.RLock()
	entry := gate.accounts[string(name)]
	ready := time.Since(gate.ready) < 5*time.Second
	gate.mu.RUnlock()
	permitted := false
	if head[0] == 1 && ready && entry != nil {
		entry.mu.Lock()
		permitted = entry.current.number("enabled") == 1 && same(string(password), entry.current.text("client_uuid")) &&
			(!entry.blocked() || entry.usage.day != shanghaiDay() && entry.current.number("daily_bytes") > 0 &&
				(entry.current.number("total_bytes") == 0 || entry.usage.total < entry.current.number("total_bytes")))
		if permitted {
			entry.connections[connection] = cancel
		}
		entry.mu.Unlock()
	}
	if !permitted {
		connection.Write([]byte{1, 1})
		return
	}
	defer func() { entry.mu.Lock(); delete(entry.connections, connection); entry.mu.Unlock() }()
	if _, err := connection.Write([]byte{1, 0}); err != nil {
		return
	}
	request, err := readN(reader, 3)
	if err != nil {
		return
	}
	host, port, err := socksAddress(reader)
	if err != nil {
		return
	}
	if request[0] != 5 || request[2] != 0 {
		return
	}
	switch request[1] {
	case 1:
		gate.connect(ctx, connection, reader, entry, host, port)
	case 3:
		gate.associate(ctx, connection, reader, entry)
	default:
		replySOCKS(connection, 7, netip.AddrPortFrom(netip.IPv4Unspecified(), 0))
	}
}

func replySOCKS(conn net.Conn, status byte, address netip.AddrPort) error {
	_, err := conn.Write(append([]byte{5, status, 0}, socksEndpoint(address)...))
	return err
}

func (gate *gateway) connect(ctx context.Context, client net.Conn, reader *bufio.Reader, entry *account, host string, port uint16) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := resolve(dialCtx, host, port)
	local := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)
	if err != nil {
		replySOCKS(client, 4, local)
		return
	}
	var upstream net.Conn
	dialer := net.Dialer{}
	for _, ip := range addresses {
		upstream, err = dialer.DialContext(dialCtx, "tcp", netip.AddrPortFrom(ip.Unmap(), port).String())
		if err == nil {
			break
		}
	}
	if err != nil {
		replySOCKS(client, 5, local)
		return
	}
	defer upstream.Close()
	stop := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stop()
	if replySOCKS(client, 0, local) != nil {
		return
	}
	client.SetDeadline(time.Time{})
	var pipes sync.WaitGroup
	pipes.Go(func() {
		if gate.pipe(ctx, entry, client, upstream, reader) != nil {
			client.Close()
			upstream.Close()
		}
	})
	if gate.pipe(ctx, entry, upstream, client, upstream) != nil {
		client.Close()
		upstream.Close()
	}
	pipes.Wait()
}

func (gate *gateway) pipe(ctx context.Context, entry *account, source, destination net.Conn, reader io.Reader) error {
	buffer := make([]byte, 32768)
	for {
		source.SetReadDeadline(time.Now().Add(300 * time.Second))
		size, err := reader.Read(buffer)
		for offset := 0; offset < size; {
			count, err := gate.take(ctx, entry, size-offset, false)
			if err != nil {
				return err
			}
			destination.SetWriteDeadline(time.Now().Add(30 * time.Second))
			written, err := destination.Write(buffer[offset : offset+count])
			offset += written
			if err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return destination.(*net.TCPConn).CloseWrite()
		}
		if err != nil {
			return err
		}
	}
}

func (gate *gateway) associate(ctx context.Context, client net.Conn, reader *bufio.Reader, entry *account) {
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	defer relay.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { relay.Close() })
	defer stop()
	if replySOCKS(client, 0, relay.LocalAddr().(*net.UDPAddr).AddrPort()) != nil {
		return
	}
	client.SetDeadline(time.Time{})
	var workers sync.WaitGroup
	workers.Go(func() { io.Copy(io.Discard, reader); cancel() })
	peers := map[string]*net.UDPConn{}
	defer func() {
		cancel()
		client.Close()
		for _, peer := range peers {
			peer.Close()
		}
		workers.Wait()
	}()
	buffer := make([]byte, 65535)
	var source netip.AddrPort
	packetReader := bufio.NewReader(bytes.NewReader(nil))
	for {
		size, from, err := relay.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		if size < 8 || !from.Addr().IsLoopback() || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 || source.IsValid() && from != source {
			continue
		}
		raw := bytes.NewReader(buffer[3:size])
		packetReader.Reset(raw)
		host, port, err := socksAddress(packetReader)
		offset := size - packetReader.Buffered() - raw.Len()
		if err != nil || offset >= size {
			continue
		}
		source = from
		key := net.JoinHostPort(host, strconv.Itoa(int(port)))
		peer := peers[key]
		if peer == nil {
			if len(peers) >= 64 {
				continue
			}
			resolveCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			addresses, err := resolve(resolveCtx, host, port)
			stop()
			if err != nil {
				continue
			}
			address := netip.AddrPortFrom(addresses[0].Unmap(), port)
			peer, err = net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(address))
			if err != nil {
				continue
			}
			peers[key] = peer
			workers.Go(func() {
				header := append([]byte{0, 0, 0}, socksEndpoint(address)...)
				response := make([]byte, 65535)
				copy(response, header)
				for {
					n, err := peer.Read(response[len(header):])
					if err != nil {
						return
					}
					if _, err := gate.take(ctx, entry, n, true); err != nil {
						if errors.Is(err, quotaExceeded) {
							cancel()
						}
						return
					}
					if _, err := relay.WriteToUDPAddrPort(response[:len(header)+n], from); err != nil {
						return
					}
				}
			})
		}
		if _, err := gate.take(ctx, entry, size-offset, true); err != nil {
			return
		}
		peer.Write(buffer[offset:size])
	}
}
