// Package discovery finds the LP10 on the LAN with a one-shot multicast-DNS
// query — no dependency, and nothing bound to :5353 (which the OS responder
// already owns). The query sets the unicast-response (QU) bit, so responders
// reply straight to our ephemeral port; we never join the multicast group.
//
// The device's AirPlay daemon advertises _raop._tcp with the model in the TXT
// (am=LP10) and "<MAC>@<FriendlyName>" as the instance, so one query yields the
// fingerprint that distinguishes our LP10 from other speakers, plus the SRV
// target (its .local host) and the A record (its current IP).
package discovery

import (
	"net"
	"sort"
	"strings"
	"time"
)

const (
	mdnsAddr  = "224.0.0.251:5353"
	service   = "_raop._tcp.local" // RAOP: TXT am=<model>, instance <MAC>@<name>
	modelLP10 = "LP10"

	typeA   uint16 = 1
	typePTR uint16 = 12
	typeTXT uint16 = 16
	typeSRV uint16 = 33

	classQU uint16 = 0x8000 // unicast-response-requested, OR'd into the question class
)

// Device is a discovered LinkPlay/Arylic endpoint.
type Device struct {
	Name  string // friendly name (after '@' in the RAOP instance), e.g. "Living"
	Model string // TXT am= value, e.g. "LP10"
	MAC   string // before '@' in the instance, e.g. "AABBCCDDEEFF"
	Host  string // SRV target (.local host), e.g. "Living.local"
	IP    net.IP // first IPv4 A record, when one arrived
}

// Addr is the address to connect to: the IP when known, else the .local host
// (which the OS resolver handles on macOS).
func (d Device) Addr() string {
	if len(d.IP) > 0 {
		return d.IP.String()
	}
	return strings.TrimSuffix(d.Host, ".")
}

// FindLP10 sends one mDNS query and watches for replies up to timeout, returning
// the LP10 whose friendly name matches nameHint (a substring match against, say,
// the config "name"), or the sole/first LP10 otherwise. It returns early the
// moment a fully-resolved candidate (with an IP) arrives, so a present device is
// usually found in well under 100ms; absence costs the full timeout.
func FindLP10(nameHint string, timeout time.Duration) (Device, bool) {
	raddr, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return Device{}, false
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return Device{}, false
	}
	defer conn.Close()
	if _, err := conn.WriteToUDP(buildQuery(service, typePTR), raddr); err != nil {
		return Device{}, false
	}

	col := newCollector()
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 9000)
	for {
		n, _, rerr := conn.ReadFromUDP(buf)
		if n > 0 {
			if recs, ok := parsePacket(buf[:n]); ok {
				col.add(recs)
				if d, ok := pickLP10(col.devices(), nameHint); ok && len(d.IP) > 0 {
					return d, true // complete candidate — stop early
				}
			}
		}
		if rerr != nil { // deadline or socket error
			break
		}
	}
	return pickLP10(col.devices(), nameHint) // timed out: accept a host-only match too
}

// ---- selection --------------------------------------------------------------

func pickLP10(ds []Device, nameHint string) (Device, bool) {
	var lp []Device
	for _, d := range ds {
		if strings.EqualFold(d.Model, modelLP10) && (len(d.IP) > 0 || d.Host != "") {
			lp = append(lp, d)
		}
	}
	if len(lp) == 0 {
		return Device{}, false
	}
	// stable order so an unhinted pick among several LP10s is deterministic
	sort.Slice(lp, func(i, j int) bool {
		if lp[i].Name != lp[j].Name {
			return lp[i].Name < lp[j].Name
		}
		return lp[i].MAC < lp[j].MAC
	})
	if nameHint != "" {
		for _, d := range lp {
			if d.Name != "" && strings.Contains(strings.ToLower(nameHint), strings.ToLower(d.Name)) {
				return d, true
			}
		}
	}
	return lp[0], true
}

// ---- record collection ------------------------------------------------------

type collector struct {
	instances map[string]struct{}          // PTR targets under the service
	srv       map[string]string            // instance -> SRV target host
	txt       map[string]map[string]string // instance -> TXT key/value
	a         map[string][]net.IP          // host (lowercased) -> A records
}

func newCollector() *collector {
	return &collector{
		instances: map[string]struct{}{},
		srv:       map[string]string{},
		txt:       map[string]map[string]string{},
		a:         map[string][]net.IP{},
	}
}

func (c *collector) add(recs []rr) {
	for _, r := range recs {
		switch r.typ {
		case typePTR:
			if strings.EqualFold(r.name, service) && r.target != "" {
				c.instances[r.target] = struct{}{}
			}
		case typeSRV:
			if r.target != "" {
				c.srv[r.name] = r.target
			}
		case typeTXT:
			m := c.txt[r.name]
			if m == nil {
				m = map[string]string{}
				c.txt[r.name] = m
			}
			for _, kv := range r.txt {
				if k, v, ok := strings.Cut(kv, "="); ok {
					m[strings.ToLower(k)] = v
				}
			}
		case typeA:
			h := strings.ToLower(strings.TrimSuffix(r.name, "."))
			c.a[h] = append(c.a[h], r.ip)
		}
	}
}

func (c *collector) devices() []Device {
	var ds []Device
	for inst := range c.instances {
		label := strings.TrimSuffix(inst, "."+service) // "<MAC>@<name>"
		mac, name := label, ""
		if i := strings.LastIndex(label, "@"); i >= 0 {
			mac, name = label[:i], label[i+1:]
		}
		d := Device{Name: name, MAC: mac}
		if m, ok := c.txt[inst]; ok {
			d.Model = m["am"]
		}
		if h, ok := c.srv[inst]; ok {
			d.Host = h
			if ips := c.a[strings.ToLower(strings.TrimSuffix(h, "."))]; len(ips) > 0 {
				d.IP = ips[0]
			}
		}
		ds = append(ds, d)
	}
	return ds
}

// ---- DNS wire format --------------------------------------------------------

type rr struct {
	name   string
	typ    uint16
	target string   // PTR/SRV target name
	txt    []string // TXT strings
	ip     net.IP   // A address
}

func be16(b []byte, i int) uint16 { return uint16(b[i])<<8 | uint16(b[i+1]) }

func encodeName(name string) []byte {
	var b []byte
	for _, lbl := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if lbl == "" {
			continue
		}
		b = append(b, byte(len(lbl)))
		b = append(b, lbl...)
	}
	return append(b, 0)
}

func buildQuery(name string, qtype uint16) []byte {
	msg := make([]byte, 12) // header: id=0, flags=0, counts=0…
	msg[5] = 1              // QDCOUNT = 1
	msg = append(msg, encodeName(name)...)
	msg = append(msg, byte(qtype>>8), byte(qtype))
	q := classQU | 1 // QU | IN
	return append(msg, byte(q>>8), byte(q))
}

// parseName decodes a (possibly compressed) name starting at off, returning the
// dotted name and the offset of the byte after the name in the wire stream.
func parseName(msg []byte, off int) (string, int, bool) {
	var sb strings.Builder
	next, jumped, jumps := -1, false, 0
	for {
		if off < 0 || off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		switch {
		case l == 0:
			off++
			if !jumped {
				next = off
			}
			return sb.String(), next, true
		case l&0xC0 == 0xC0: // compression pointer
			if off+1 >= len(msg) {
				return "", 0, false
			}
			if !jumped {
				next = off + 2
			}
			jumped = true
			if jumps++; jumps > 64 {
				return "", 0, false
			}
			off = (l&0x3F)<<8 | int(msg[off+1])
		default:
			off++
			if off+l > len(msg) {
				return "", 0, false
			}
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.Write(msg[off : off+l])
			off += l
		}
	}
}

func parseTXT(rd []byte) []string {
	var out []string
	for i := 0; i < len(rd); {
		l := int(rd[i])
		i++
		if i+l > len(rd) {
			break
		}
		out = append(out, string(rd[i:i+l]))
		i += l
	}
	return out
}

// parsePacket extracts the resource records (PTR/SRV/TXT/A) from one mDNS
// message, across the answer, authority, and additional sections.
func parsePacket(msg []byte) ([]rr, bool) {
	if len(msg) < 12 {
		return nil, false
	}
	qd, an, ns, ar := int(be16(msg, 4)), int(be16(msg, 6)), int(be16(msg, 8)), int(be16(msg, 10))
	off := 12
	for i := 0; i < qd; i++ { // skip questions
		_, no, ok := parseName(msg, off)
		if !ok {
			return nil, false
		}
		off = no + 4 // QTYPE + QCLASS
		if off > len(msg) {
			return nil, false
		}
	}
	var out []rr
	for i := 0; i < an+ns+ar; i++ {
		name, no, ok := parseName(msg, off)
		if !ok || no+10 > len(msg) {
			return nil, false
		}
		typ := be16(msg, no)
		rdlen := int(be16(msg, no+8))
		rdStart := no + 10
		if rdStart+rdlen > len(msg) {
			return nil, false
		}
		r := rr{name: name, typ: typ}
		switch typ {
		case typePTR:
			r.target, _, _ = parseName(msg, rdStart)
		case typeSRV:
			if rdlen >= 7 { // priority(2) weight(2) port(2) target
				r.target, _, _ = parseName(msg, rdStart+6)
			}
		case typeTXT:
			r.txt = parseTXT(msg[rdStart : rdStart+rdlen])
		case typeA:
			if rdlen == 4 {
				r.ip = net.IPv4(msg[rdStart], msg[rdStart+1], msg[rdStart+2], msg[rdStart+3])
			}
		}
		out = append(out, r)
		off = rdStart + rdlen
	}
	return out, true
}
