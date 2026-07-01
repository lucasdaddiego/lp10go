package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// diagCardsMinW is the inner width at/above which the diagnostics overlay uses the
// two-column card grid; below it, the single-column stacked layout (which fits a
// narrow terminal and degrades gracefully) is used instead.
const diagCardsMinW = 100

// diagFooter is the overlay's bottom help line (both layouts).
const diagFooter = "live · any key returns to the dashboard"

// ---- shared severity model -----------------------------------------------------
//
// Health thresholds, lower-is-better: sev(v, thr) reads 0 (good) below thr[0],
// 1 (warn) below thr[1], 2 (bad) at/above. One table shared by both layouts so a
// stacked gauge, a cards gauge, the vitals line, and the verdict rollup can never
// disagree on where "warn" starts.
var (
	thrCPU    = [2]float64{60, 85} // % of all cores (1m load / NCPU)
	thrMem    = [2]float64{70, 88} // % used
	thrTemp   = [2]float64{60, 75} // °C SoC
	thrData   = [2]float64{80, 92} // % of /lsync used
	thrRx     = [2]float64{3, 8}   // seconds since the last framed record
	thrSignal = [2]float64{60, 72} // Wi-Fi signal as -dBm (-41 good, -72 warn)
)

func sev(v float64, thr [2]float64) int {
	switch {
	case v < thr[0]:
		return 0
	case v < thr[1]:
		return 1
	default:
		return 2
	}
}

// sevPens maps a severity to its pen: good (accent) · warn (amber) · bad (red).
func (m *model) sevPens() [3]lipgloss.Style { return [3]lipgloss.Style{m.sty.sAcc, stWarn, stRed} }

// sevPen picks the pen for a value against its threshold pair.
func (m *model) sevPen(v float64, thr [2]float64) lipgloss.Style { return m.sevPens()[sev(v, thr)] }

// ---- shared collectors (both layouts read the same derived state) --------------

// diagIdentity is the device section's readout, shared by both diagnostics
// layouts (renderDiagStacked / renderDiagCards) so the two can't drift apart:
// identity ONLY — what the box is, not how it's doing or how it's reached.
// Wire facts live in the connection/network sections (host, mac) and runtime
// state in resources (uptime). The first row of fields defaults to "—" (always
// shown); the second row stays "" until the device reports it (regs 90/92),
// and its rows render only then.
type diagIdentity struct {
	model, os, fw, build  string
	name, serial, bt, mcu string
}

// collectIdentity derives the identity strings from the sysinfo/devinfo/details
// (any may be nil).
func collectIdentity(si *protocol.SysInfo, dev *protocol.DevInfo, dt *protocol.DevDetails) diagIdentity {
	d := diagIdentity{model: "—", os: "—", fw: "—", build: "—"}
	if si != nil {
		if si.FW != "" {
			d.fw, d.model = si.FW, "Arylic "+firstSeg(si.FW, '_')
		}
		if si.OS != "" {
			d.os = strings.Replace(si.OS, "-", " ", 1)
			if si.NCPU != "" {
				d.os += " · " + si.NCPU + " cores"
			}
		}
	}
	if dev != nil {
		if dev.Platform != "" && d.model != "—" {
			d.model += " · " + dev.Platform
		}
		if dev.Build != "" {
			d.build = dev.Build
			if dev.App != "" {
				d.build += " · app " + dev.App
			}
		}
		d.name = dev.Name
	}
	if dt != nil {
		d.serial, d.bt = dt.Serial, dt.BTMAC
		if dt.MCU != "" {
			d.mcu = "v" + dt.MCU
		}
		if dt.FW != "" {
			d.fw = dt.FW // the fuller string — carries the trailing sub-version
		}
	}
	return d
}

// hostReadout is the connection section's target line: how lp10 reaches the
// device — the ssh user @ the configured host, upgraded to the resolved IP
// once @@i reports it, tagged when mDNS discovery found the box.
func (m *model) hostReadout(dev *protocol.DevInfo) string {
	h := m.cfg.User + "@" + m.cfg.Host
	if dev != nil && dev.IP != "" {
		h = m.cfg.User + "@" + dev.IP
	}
	if m.cfg.Discovered {
		h += " · mDNS"
	}
	return h
}

// sshReadout is the connection section's stream line: how fresh the framed
// records are, plus the connect-attempt count.
func (m *model) sshReadout(ls diagLinkStatus, att int) string {
	tail := m.sty.sTxt.Render(fmt.Sprintf(" · %d %s", att, ls.attWord))
	if ls.rxTxt == "—" { // nothing framed yet — say so instead of "rx — ago"
		return m.sty.sDim.Render("no data yet") + tail
	}
	return m.sty.sTxt.Render("rx ") + ls.rxPen.Render(ls.rxTxt) + m.sty.sTxt.Render(" ago") + tail
}

// tunnelReadout is the connection section's :2018 line (the EQ / Max-Vol
// control tunnel): the port and its live/down state.
func (m *model) tunnelReadout(ls diagLinkStatus) string {
	return m.sty.sTxt.Render(":2018 · ") + ls.tunPen.Render(ls.tunTxt)
}

// errReadout renders the interface error/drop counters as session deltas:
// calm dim zeros, amber the moment a counter grows while connected.
func (m *model) errReadout(ns protocol.NetStat) string {
	cell := func(label string, v int64) string {
		pen := m.sty.sDim
		if v > 0 {
			pen = stWarn
		}
		return m.sty.sDim.Render(label+" ") + pen.Render(strconv.FormatInt(v, 10))
	}
	sep := m.sty.sDmr.Render(" · ")
	return cell("rx", ns.RxErrs) + sep + cell("tx", ns.TxErrs) + sep + cell("drop", ns.Drops) +
		m.sty.sDmr.Render(" · session")
}

// multiroomReadout renders the group state: "solo", or the linked device count.
func (m *model) multiroomReadout(mr *protocol.Multiroom) string {
	if mr.Devices == 0 {
		return m.sty.sTxt.Render("solo")
	}
	word := "devices"
	if mr.Devices == 1 {
		word = "device"
	}
	return m.sty.sAcc.Render(fmt.Sprintf("linked · %d %s", mr.Devices, word))
}

// diagVitals is the parsed live-numeric readout shared by both layouts: raw
// numbers only — each layout formats its own labels and detail strings.
type diagVitals struct {
	haveCPU bool
	cpuFrac float64  // 1m load / cores
	loads   []string // the raw loadavg triplet, for the detail strings

	haveMem          bool
	memUf            float64 // fraction used
	availKB, totalKB int

	haveTemp bool
	tempC    int

	haveData       bool
	dataUf         float64 // fraction of /lsync used
	usedKB, dataKB int

	haveBuf bool
	bufFill float64 // ALSA ring fill fraction
	bufSev  int     // inverted health: a FULL ring is healthy

	playing bool // ALSA reports RUNNING — gates the buffer's health meaning
}

// collectVitals parses the @@s/@@i numerics both layouts gauge (either source may
// be nil; the have* flags gate each reading).
func collectVitals(si *protocol.SysInfo, dev *protocol.DevInfo) diagVitals {
	var v diagVitals
	if si != nil {
		v.loads = strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(v.loads) >= 1 {
			if l1, err := strconv.ParseFloat(v.loads[0], 64); err == nil {
				v.cpuFrac, v.haveCPU = l1/float64(nc), true
			}
		}
		av, e1 := strconv.Atoi(si.Avail)
		tot, e2 := strconv.Atoi(si.Total)
		if e1 == nil && e2 == nil && tot > 0 {
			v.memUf, v.availKB, v.totalKB, v.haveMem = float64(tot-av)/float64(tot), av, tot, true
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			v.tempC, v.haveTemp = mc/1000, true
		}
		if si.BufAvail != "" && si.BufSize != "" {
			if a, e1 := strconv.Atoi(si.BufAvail); e1 == nil {
				if bs, e2 := strconv.Atoi(si.BufSize); e2 == nil && bs > 0 {
					v.bufFill, v.haveBuf = max(float64(bs-a)/float64(bs), 0), true
					switch { // buffer health is inverted: a FULL ring is healthy
					case v.bufFill >= 0.5:
						v.bufSev = 0
					case v.bufFill >= 0.25:
						v.bufSev = 1
					default:
						v.bufSev = 2
					}
				}
			}
		}
		v.playing = si.PcmState == "RUNNING"
	}
	if dev != nil {
		u, e1 := strconv.Atoi(dev.DataUsed)
		tt, e2 := strconv.Atoi(dev.DataTotal)
		if e1 == nil && e2 == nil && tt > 0 {
			v.dataUf, v.usedKB, v.dataKB, v.haveData = float64(u)/float64(tt), u, tt, true
		}
	}
	return v
}

// diagLinkStatus is lp10's own link readout — ssh stream freshness, the attempt
// count's noun, and the :2018 tunnel state — shared verbatim by both layouts.
type diagLinkStatus struct {
	rxTxt   string
	rxPen   lipgloss.Style
	attWord string
	tunTxt  string
	tunPen  lipgloss.Style
}

func (m *model) linkStatus(lastRx, now time.Time, att int, eqConn bool) diagLinkStatus {
	ls := diagLinkStatus{rxTxt: "—", rxPen: m.sty.sDim, attWord: "attempts", tunTxt: "down", tunPen: stRed}
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		ls.rxTxt, ls.rxPen = fmt.Sprintf("%.1fs", secs), m.sevPen(secs, thrRx)
	}
	if att == 1 {
		ls.attWord = "attempt"
	}
	if eqConn {
		ls.tunTxt, ls.tunPen = "live", m.sty.sAcc
	}
	return ls
}

// diagStatus is the connection light + clock on the masthead's right. The
// silence window matches the watchdog's threshold (not a tighter one): the
// device's idle loop legitimately drops to a ~3s poll cadence, so a shorter
// window would flash "LUCI silent" between healthy low-poll frames.
func (m *model) diagStatus(connected bool, dData, now time.Time) (hr string, hrW int, silent bool) {
	clock := now.Format("15:04")
	switch {
	case !connected:
		return stWarn.Render("● disconnected"), DispW("● disconnected"), false
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		return stWarn.Render("● LUCI silent · " + clock), DispW("● LUCI silent · " + clock), true
	default:
		return m.sty.sAcc.Render("●") + m.sty.sDim.Render(" "+clock), DispW("● " + clock), false
	}
}

// wifiBand renders the " · ch N · 2.4|5 GHz" suffix from the @@i freq (MHz), or
// "" when the frequency is unknown.
func wifiBand(freq string) string {
	f, err := strconv.Atoi(freq)
	if err != nil || f <= 0 {
		return ""
	}
	b := " · 2.4 GHz"
	if f >= 5000 {
		b = " · 5 GHz"
	}
	return fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
}

// ethDetail renders the " · N Mbit/s · full duplex" suffix from the @@i link fields.
func ethDetail(speed, duplex string) string {
	detail := ""
	if sp, err := strconv.Atoi(speed); err == nil && sp > 0 {
		detail += fmt.Sprintf(" · %d Mbit/s", sp)
	}
	if duplex != "" {
		detail += " · " + duplex + " duplex"
	}
	return detail
}

// diagFormat is the source-stream descriptor for the audio section — "Ogg ·
// 44.1 kHz · 2 ch" — or "—" when nothing is playing.
func diagFormat(tr protocol.Track) string {
	if tr == nil {
		return "—"
	}
	var ps []string
	if q := Quality(tr); q != "" {
		ps = append(ps, q)
	}
	if ch := tr.GetInt("ChannelCount"); ch > 0 {
		ps = append(ps, fmt.Sprintf("%d ch", ch))
	}
	if len(ps) == 0 {
		return "—"
	}
	return strings.Join(ps, " · ")
}

// bufMeter picks the buffer gauge's pen + detail word, shared by both layouts:
// the ring is a health signal only WHILE PLAYING ("NN% full", severity-
// coloured); an empty ring on an idle device is normal ("idle", neutral).
func (m *model) bufMeter(vit diagVitals) (lipgloss.Style, string) {
	if vit.playing {
		return m.sevPens()[vit.bufSev], "full"
	}
	return m.sty.sDim, "idle"
}

// dacReadout is the audio section's output line — the DAC's actual rate /
// format / channels, tagged live while ALSA reports RUNNING — or "" until @@s
// carries a rate.
func (m *model) dacReadout(si *protocol.SysInfo, playing bool) string {
	if si == nil || si.DacRate == "" {
		return ""
	}
	rate := si.DacRate
	if hz, err := strconv.Atoi(si.DacRate); err == nil {
		rate = fmtKHz(hz)
	}
	parts := []string{rate}
	if si.DacFmt != "" {
		parts = append(parts, si.DacFmt)
	}
	if si.DacCh != "" {
		parts = append(parts, si.DacCh+"ch")
	}
	out := m.sty.sTxt.Render(strings.Join(parts, " · "))
	if playing {
		out += m.sty.sAcc.Render(" ● live")
	}
	return out
}

// tasksReadout is the resources section's scheduler line from /proc's
// running/total pair, or "" when the sample lacks one.
func (m *model) tasksReadout(si *protocol.SysInfo) string {
	if si == nil || si.Procs == "" {
		return ""
	}
	run, tot, ok := strings.Cut(si.Procs, "/")
	if !ok {
		return ""
	}
	return m.sty.sTxt.Render(run) + m.sty.sDim.Render(" running · ") +
		m.sty.sTxt.Render(tot) + m.sty.sDim.Render(" total")
}

// latencyPeakPen flags a genuine spike (peak well past the average), not
// baseline wobble.
func (m *model) latencyPeakPen(ps protocol.PingStat) lipgloss.Style {
	if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 {
		return stWarn
	}
	return m.sty.sDmr
}

// kv is one labelled fact; presentKVs keeps the ones the device has reported
// (empty values are the "not read yet" sentinel for the optional identity rows).
type kv struct{ k, v string }

func presentKVs(facts []kv) []kv {
	out := make([]kv, 0, len(facts))
	for _, f := range facts {
		if f.v != "" {
			out = append(out, f)
		}
	}
	return out
}

// latTarget is one responding ping target: its row label and stats.
type latTarget struct {
	name string
	ps   protocol.PingStat
}

// latencyTargets returns the responding ping targets in alphabetical name order
// (row order matches the a-z ordering of every other diag item, not hop order).
func (m *model) latencyTargets(netv protocol.NetStat) []latTarget {
	names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
	out := make([]latTarget, 0, 3)
	for i, ps := range netv.Ping {
		if ps.OK {
			out = append(out, latTarget{names[i], ps})
		}
	}
	slices.SortFunc(out, func(a, b latTarget) int { return strings.Compare(a.name, b.name) })
	return out
}

// ---- the two layouts ------------------------------------------------------------

// renderDiag picks the diagnostics layout by width: a two-column card grid on a
// wide terminal (filling the space and surfacing the audio-chain metrics), the
// stacked single-column read-out when narrow.
func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) string {
	if W >= diagCardsMinW {
		return m.renderDiagCards(s, now, W)
	}
	return m.renderDiagStacked(s, now, W)
}

func (m *model) renderDiagStacked(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	dt := m.st.DetailsView()
	netv := m.st.NetView()
	eqConn, _ := m.st.EQView()
	vit := collectVitals(si, dev)
	ls := m.linkStatus(lastRx, now, att, eqConn)

	gw := max(min(20, W-52), 8) // gauge width, leaving room for label/value/detail

	var L []string
	add := func(s string) { L = append(L, s) }

	hr, hrW, _ := m.diagStatus(s.Connected, dData, now)
	add(between(t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics"), hr, hrW, W))
	add("")

	// the titled sections a-z, each section's rows a-z — the same taxonomy as
	// the cards layout, only formatted for a narrow single column (the device
	// facts pack two per grid row, latency folds into network).
	add(m.dividerRow("audio", W))
	bufPen, bufDetail := m.bufMeter(vit)
	if vit.haveBuf {
		add(m.diagGauge("buffer", t.gaugeBar(vit.bufFill, gw, bufPen),
			bufPen.Render(fmt.Sprintf("%d%%", int(vit.bufFill*100+0.5))), "   "+bufDetail, W))
	}
	if dac := m.dacReadout(si, vit.playing); dac != "" {
		add(m.diagLine("dac", dac))
	}
	add(m.diagLine("stream", t.sTxt.Render(diagFormat(s.Track))))

	// lp10's own links to the box — these render even while the device is
	// unreachable, which is exactly when they matter.
	add(m.dividerRow("connection", W))
	add(m.diagLine("host", t.sTxt.Render(m.hostReadout(dev))))
	add(m.diagLine("ssh", m.sshReadout(ls, att)))
	add(m.diagLine("tunnel", m.tunnelReadout(ls)))

	// identity grid: the device facts in alphabetical reading order, two per
	// row (the reg-90/92 extras join once reported).
	add(m.dividerRow("device", W))
	id := collectIdentity(si, dev, dt)
	idFacts := presentKVs([]kv{
		{"bt", id.bt},
		{"build", id.build},
		{"firmware", id.fw},
		{"mcu", id.mcu},
		{"model", id.model},
		{"name", id.name},
		{"os", id.os},
		{"serial", id.serial},
	})
	for i := 0; i < len(idFacts); i += 2 {
		k2, v2 := "", ""
		if i+1 < len(idFacts) {
			k2, v2 = idFacts[i+1].k, idFacts[i+1].v
		}
		add(m.gridRow(idFacts[i].k, idFacts[i].v, k2, v2, W))
	}

	add(m.dividerRow("hardware", W))
	for _, h := range confHardware {
		add(m.diagLine(h.k, t.sTxt.Render(Clip(h.v, max(1, W-diagLabelW)))))
	}

	add(m.dividerRow("network", W))
	haveDev := dev != nil && (dev.IP != "" || dev.Net != "")
	if haveDev {
		add(m.diagLine("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			add(m.diagLine("dns", t.sTxt.Render(dev.DNS)))
		}
	}
	if netv.ErrsOK {
		add(m.diagLine("errors", m.errReadout(netv)))
	}
	if haveDev {
		// one row per responding target: avg · jitter · peak over the rolling window.
		latLabel := "latency"
		for _, lt := range m.latencyTargets(netv) {
			add(m.diagLine(latLabel, m.latencyRow(lt.name, lt.ps)))
			latLabel = ""
		}
		if dev.Net == "wifi" {
			add(m.diagLine("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(wifiBand(dev.Freq))))
		} else {
			add(m.diagLine("link", t.sBri.Render("ethernet")+t.sDim.Render(ethDetail(dev.Speed, dev.Duplex))))
		}
		if dev.MAC != "" {
			add(m.diagLine("mac", t.sTxt.Render(dev.MAC)))
		}
	}
	if mr := m.st.MultiroomView(); mr != nil {
		add(m.diagLine("multiroom", m.multiroomReadout(mr)))
	}
	if haveDev && dev.Net == "wifi" && si != nil {
		if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
			pen := m.sevPen(float64(-dbm), thrSignal)
			valTxt := fmt.Sprintf("%d dBm", dbm)
			detail := ""
			if dev.Rate != "" {
				detail = "   " + dev.Rate + " Mbit/s"
			}
			if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
				detail += fmt.Sprintf("  · link %d/70", lq)
			}
			add(m.diagGauge("signal", t.gaugeBar(float64(dbm+90)/60, gw, pen), pen.Render(valTxt), detail, W))
		}
	}
	if haveDev && netv.RatesOK {
		add(m.diagLine("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+
			t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
	}

	add(m.dividerRow("resources", W))
	if vit.haveCPU {
		pen := m.sevPen(vit.cpuFrac*100, thrCPU)
		detail := "   1m " + vit.loads[0]
		if len(vit.loads) >= 3 {
			detail += " · 5m " + vit.loads[1] + " · 15m " + vit.loads[2]
		}
		add(m.diagGauge("cpu", t.gaugeBar(vit.cpuFrac, gw, pen),
			pen.Render(fmt.Sprintf("%d%%", int(vit.cpuFrac*100+0.5))), detail, W))
	}
	if vit.haveMem {
		pen := m.sevPen(vit.memUf*100, thrMem)
		add(m.diagGauge("memory", t.gaugeBar(vit.memUf, gw, pen),
			pen.Render(fmt.Sprintf("%d%%", int(vit.memUf*100+0.5))),
			fmt.Sprintf("   %d / %d MB free", vit.availKB/1024, vit.totalKB/1024), W))
	}
	if vit.haveData {
		pen := m.sevPen(vit.dataUf*100, thrData)
		add(m.diagGauge("storage", t.gaugeBar(vit.dataUf, gw, pen),
			pen.Render(fmt.Sprintf("%d%%", int(vit.dataUf*100+0.5))),
			fmt.Sprintf("   %d / %d MB used · /lsync", vit.usedKB/1024, vit.dataKB/1024), W))
	}
	if tasks := m.tasksReadout(si); tasks != "" {
		add(m.diagLine("tasks", tasks))
	}
	if vit.haveTemp {
		pen := m.sevPen(float64(vit.tempC), thrTemp)
		add(m.diagGauge("temp", t.gaugeBar(float64(vit.tempC)/85, gw, pen),
			pen.Render(fmt.Sprintf("%d °C", vit.tempC)), "   SoC", W))
	}
	if si != nil {
		if up := fmtUptime(si.Up); up != "—" {
			add(m.diagLine("uptime", t.sTxt.Render(up)))
		}
	}

	add(m.dividerRow("services", W))
	for _, r := range m.serviceStrip(W) {
		add(r)
	}

	// footer (and any device error) pins to the bottom; the gap fills the frame
	var tail []string
	if derr != "" {
		// prettified, not the raw ssh dump — the overlay already shows the state
		// (disconnected · tunnel down · N attempts), so keep only the readable reason
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	tail = append(tail, t.sDmr.Render(diagFooter))

	// on a too-short pane, trim the read-out from the bottom and flag it
	if room := m.rows - 2 - len(tail); room > 2 && len(L) > room {
		L = L[:room]
		L[room-1] = t.sDmr.Render("… resize for more")
	}
	return strings.Join(frameBody(L, tail, m.rows-2, false), "\n") // top-aligned: read-out hugs the top, footer stays pinned below
}

// renderDiagCards is the wide diagnostics layout: a minimal masthead — the
// title, a health VERDICT (the worst-of rollup of the live signals), and the
// clock — over a heavy rule, then the detail in two boxless, ruled columns.
// The sections run in alphabetical order, flowing down the left column and
// continuing down the right, with the split chosen to balance the two heights.
// No card boxes — the section rule + a left gutter of aligned labels carry the
// structure, so it reads faster and sits a couple lines shorter than the old
// 7-card grid.
func (m *model) renderDiagCards(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	dt := m.st.DetailsView()
	netv := m.st.NetView()
	eqConn, _ := m.st.EQView()
	vit := collectVitals(si, dev)

	worst := 0
	bump := func(sv int) {
		if sv > worst {
			worst = sv
		}
	}

	// two equal columns; the sections flow alphabetically down the left column
	// and continue down the right, split where the two heights balance best.
	const (
		gutter = 4
		gwc    = 12 // gauge width
	)
	colW := (W - gutter) / 2
	rightW := W - gutter - colW // absorbs the odd column
	inner := colW - 2           // rows sit under a 2-space indent

	kvP := func(inner int, label, value string, pen lipgloss.Style) string {
		return t.sDim.Render(label) + labelGap(label, diagLabelW) + pen.Render(Clip(value, max(1, inner-diagLabelW)))
	}
	kvR := func(label, styled string) string { return t.sDim.Render(label) + labelGap(label, diagLabelW) + styled }
	cg := func(inner int, label, valuePlain string, frac float64, pen lipgloss.Style, detail string) string {
		out := t.sDim.Render(label) + labelGap(label, diagLabelW) + t.gaugeBar(frac, gwc, pen) + "  " + pen.Render(valuePlain)
		if detail != "" {
			if d := Clip(detail, inner-(diagLabelW+gwc+2+DispW(valuePlain))-1); d != "" {
				out += " " + t.sDmr.Render(d)
			}
		}
		return out
	}
	// boxless section: a left-anchored "─ title ─────" head + indented rows.
	sectionHead := func(title string, w int) string {
		fill := max(w-3-DispW(title), 0) // "─ " + title + " "
		return t.sDmr.Render("─ ") + t.sAcc.Bold(true).Render(title) + t.sDmr.Render(" "+strings.Repeat("─", fill))
	}
	section := func(title string, rows []string, w int) []string {
		out := make([]string, 0, len(rows)+1)
		out = append(out, sectionHead(title, w))
		for _, r := range rows {
			out = append(out, "  "+clipStyled(r, w-2))
		}
		return out
	}

	// per-layout detail strings for the shared vitals (the cards' compact forms).
	cpuDetail := ""
	if vit.haveCPU {
		cpuDetail = "1m " + vit.loads[0]
		if si.CpuKHz != "" {
			if khz, e := strconv.Atoi(si.CpuKHz); e == nil {
				cpuDetail += fmt.Sprintf(" · %d MHz", khz/1000)
			}
		}
	}
	memDetail := fmt.Sprintf("%d/%d MB free", vit.availKB/1024, vit.totalKB/1024)
	dataDetail := fmt.Sprintf("%d/%d MB /lsync", vit.usedKB/1024, vit.dataKB/1024)

	// the buffer ring is only a health signal WHILE PLAYING — an empty ring on an
	// idle/paused device is normal, so it stays neutral and out of the verdict then.
	// The detail word says what the number is ("79% full" of the ALSA ring buffer)
	// or why there isn't one ("idle").
	bufPen, bufDetail := m.bufMeter(vit)

	// roll the live health signals into the worst-of verdict.
	if vit.haveCPU {
		bump(sev(vit.cpuFrac*100, thrCPU))
	}
	if vit.haveMem {
		bump(sev(vit.memUf*100, thrMem))
	}
	if vit.haveTemp {
		bump(sev(float64(vit.tempC), thrTemp))
	}
	if vit.haveData {
		bump(sev(vit.dataUf*100, thrData))
	}
	if vit.haveBuf && vit.playing {
		bump(vit.bufSev)
	}
	if !lastRx.IsZero() {
		bump(sev(now.Sub(lastRx).Seconds(), thrRx))
	}

	// ---- status line: the title + the health verdict (left), the connection
	// light + clock (right). Just those — every live number lives in its
	// section below; the masthead answers only "is it OK, and is this fresh".
	// (Volume/EQ appear nowhere in the overlay: settings, not diagnostics.) ----
	hr, hrW, silent := m.diagStatus(s.Connected, dData, now)
	left, leftHdrW := t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics")
	if s.Connected && !silent { // a fresh device gets a one-glance health verdict
		verWord, verPen := "healthy", t.sAcc
		switch worst {
		case 1:
			verWord, verPen = "warn", stWarn
		case 2:
			verWord, verPen = "fault", stRed
		}
		vd := "● " + verWord
		left += "   " + verPen.Render(vd)
		leftHdrW += 3 + DispW(vd)
	}
	masthead := between(left, leftHdrW, hr, hrW, W)

	// ---- the section rows, each in alphabetical label order (assembled
	// alphabetically into two columns below) ----
	id := collectIdentity(si, dev, dt)
	devFacts := presentKVs([]kv{ // the reg-90/92 extras join once reported
		{"bt", id.bt},
		{"build", id.build},
		{"firmware", id.fw},
		{"mcu", id.mcu},
		{"model", id.model},
		{"name", id.name},
		{"os", id.os},
		{"serial", id.serial},
	})
	deviceRows := make([]string, 0, len(devFacts))
	for _, f := range devFacts {
		deviceRows = append(deviceRows, kvP(inner, f.k, f.v, t.sTxt))
	}

	// connection: lp10's own links to the box — host · ssh · tunnel — kept out
	// of the device/network sections because they render (and matter most)
	// even while the device itself is unreachable.
	ls := m.linkStatus(lastRx, now, att, eqConn)
	crows := []string{
		kvP(inner, "host", m.hostReadout(dev), t.sTxt),
		kvR("ssh", m.sshReadout(ls, att)),
		kvR("tunnel", m.tunnelReadout(ls)),
	}

	// network: the device's own link — address · dns · errors · link · mac ·
	// multiroom · rate · signal · traffic.
	haveDev := dev != nil && (dev.IP != "" || dev.Net != "")
	var nrows []string
	if haveDev {
		nrows = append(nrows, kvR("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			nrows = append(nrows, kvP(inner, "dns", dev.DNS, t.sTxt))
		}
	}
	if netv.ErrsOK {
		nrows = append(nrows, kvR("errors", m.errReadout(netv)))
	}
	if haveDev {
		if dev.Net == "wifi" {
			nrows = append(nrows, kvR("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(wifiBand(dev.Freq))))
		} else {
			nrows = append(nrows, kvR("link", t.sBri.Render("ethernet")+t.sDim.Render(ethDetail(dev.Speed, dev.Duplex))))
		}
		if dev.MAC != "" {
			nrows = append(nrows, kvP(inner, "mac", dev.MAC, t.sTxt))
		}
	}
	if mr := m.st.MultiroomView(); mr != nil {
		nrows = append(nrows, kvR("multiroom", m.multiroomReadout(mr)))
	}
	if haveDev && dev.Net == "wifi" {
		if dev.Rate != "" {
			nrows = append(nrows, kvP(inner, "rate", dev.Rate+" Mbit/s", t.sTxt))
		}
		if si != nil {
			if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
				pen := m.sevPen(float64(-dbm), thrSignal)
				detail := ""
				if nz, e := strconv.Atoi(si.NoiseDBm); e == nil && nz < 0 {
					detail = fmt.Sprintf("snr %d dB", dbm-nz) // signal − noise
				} else if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
					detail = fmt.Sprintf("link %d/70", lq)
				}
				nrows = append(nrows, cg(inner, "signal", fmt.Sprintf("%d dBm", dbm), float64(dbm+90)/60, pen, detail))
			}
		}
	}
	if haveDev && netv.RatesOK {
		nrows = append(nrows, kvR("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
	}

	// latency: one row per responding target, a-z by name (shared with stacked).
	var lrows []string
	if haveDev {
		for _, lt := range m.latencyTargets(netv) {
			lrows = append(lrows, m.latencyRow(lt.name, lt.ps))
		}
	}

	hwRows := make([]string, 0, len(confHardware))
	for _, h := range confHardware {
		hwRows = append(hwRows, kvP(inner, h.k, h.v, t.sTxt))
	}

	// audio: the live playback chain — buffer · dac · stream. Volume and EQ are
	// settings, not diagnostics: they live on the dashboard and the EQ pane.
	var arows []string
	if vit.haveBuf {
		arows = append(arows, cg(inner, "buffer", fmt.Sprintf("%d%%", int(vit.bufFill*100+0.5)), vit.bufFill, bufPen, bufDetail))
	}
	if dac := m.dacReadout(si, vit.playing); dac != "" {
		arows = append(arows, kvR("dac", dac))
	}
	arows = append(arows, kvP(inner, "stream", diagFormat(s.Track), t.sTxt))

	// resources: cpu · memory · storage · tasks · temp · uptime
	var rrows []string
	if vit.haveCPU {
		rrows = append(rrows, cg(inner, "cpu", fmt.Sprintf("%d%%", int(vit.cpuFrac*100+0.5)), vit.cpuFrac, m.sevPen(vit.cpuFrac*100, thrCPU), cpuDetail))
	}
	if vit.haveMem {
		rrows = append(rrows, cg(inner, "memory", fmt.Sprintf("%d%%", int(vit.memUf*100+0.5)), vit.memUf, m.sevPen(vit.memUf*100, thrMem), memDetail))
	}
	if vit.haveData {
		rrows = append(rrows, cg(inner, "storage", fmt.Sprintf("%d%%", int(vit.dataUf*100+0.5)), vit.dataUf, m.sevPen(vit.dataUf*100, thrData), dataDetail))
	}
	if tasks := m.tasksReadout(si); tasks != "" {
		rrows = append(rrows, kvR("tasks", tasks))
	}
	if vit.haveTemp {
		rrows = append(rrows, cg(inner, "temp", fmt.Sprintf("%d °C", vit.tempC), float64(vit.tempC)/85, m.sevPen(float64(vit.tempC), thrTemp), "SoC"))
	}
	if si != nil {
		if up := fmtUptime(si.Up); up != "—" {
			rrows = append(rrows, kvP(inner, "uptime", up, t.sTxt))
		}
	}

	// ---- assemble the columns: sections in alphabetical order, flowing down the
	// left column then the right, split at the point that best balances the two
	// heights (sections with nothing to say are skipped, so the balance adapts to
	// what the device actually reported). ----
	type diagSection struct {
		title string
		rows  []string
	}
	secs := make([]diagSection, 0, 8)
	for _, sec := range []diagSection{
		{"audio", arows},
		{"connection", crows},
		{"device", deviceRows},
		{"hardware", hwRows},
		{"latency", lrows},
		{"network", nrows},
		{"resources", rrows},
		{"services", m.serviceStrip(inner)},
	} {
		if len(sec.rows) > 0 {
			secs = append(secs, sec)
		}
	}
	colH := func(ss []diagSection) int { // head + rows per section, blank line between
		h := 0
		for i, sec := range ss {
			if i > 0 {
				h++
			}
			h += 1 + len(sec.rows)
		}
		return h
	}
	split, best := 0, 1<<30
	for k := 0; k <= len(secs); k++ {
		if d := colH(secs[:k]) - colH(secs[k:]); max(d, -d) < best {
			split, best = k, max(d, -d)
		}
	}
	column := func(ss []diagSection, w int) []string {
		var out []string
		for i, sec := range ss {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, section(sec.title, sec.rows, w)...)
		}
		return out
	}
	left2 := column(secs[:split], colW)
	right2 := column(secs[split:], rightW)

	// ---- compose: the status line, a heavy rule, then the zipped columns ----
	content := []string{masthead, t.sDmr.Render(strings.Repeat("━", W))}
	gut := strings.Repeat(" ", gutter)
	blankR := strings.Repeat(" ", rightW)
	for i := 0; i < max(len(left2), len(right2)); i++ {
		l := strings.Repeat(" ", colW)
		if i < len(left2) {
			l = padVis(left2[i], colW)
		}
		r := blankR
		if i < len(right2) {
			r = padVis(right2[i], rightW)
		}
		content = append(content, l+gut+r)
	}

	// footer + a small colour legend so the verdict/ribbon hues decode at a glance.
	legend := t.sAcc.Render("●") + t.sDmr.Render(" good   ") + stWarn.Render("●") + t.sDmr.Render(" warn   ") + stRed.Render("●") + t.sDmr.Render(" fault")
	var tail []string
	if derr != "" {
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	tail = append(tail, between(t.sDmr.Render(diagFooter), DispW(diagFooter), legend, DispW("● good   ● warn   ● fault"), W))
	return strings.Join(frameBody(content, tail, m.rows-2, false), "\n")
}

// ---- device capabilities + hardware (shown in the diagnostics overlay) -------
//
// "What can this box do, and what is it" — surfaced inside the `?` overlay rather
// than a separate view, so the device identity is never shown twice. The
// streaming-capability matrix is read live from the device (the one-shot @@c block
// — running daemons via pidof, env-gated features via getenv — exposed by
// ConfView); the hardware list encodes the model's verified, invariant facts (see
// arylic-lp10-teardown.md). @@c rides the connect unconditionally, so the matrix is
// already in hand whenever the overlay opens.

// confServices is the capability matrix in display order (alphabetical by label,
// like every diag section's items) — the LP10's *marketed* streaming features
// only. LibreWireless reference-image baggage that this box doesn't actually
// offer (Roon / Alexa / Matter / QPlay — installed but env-gated off, not on
// Arylic's spec sheet; see teardown §13/§7.4) is deliberately omitted. id
// matches the @@c wire key; the on/off grouping is decided live, so each group
// row also reads a-z.
var confServices = []struct{ id, label string }{
	{"airplay", "AirPlay 2"},
	{"bt", "Bluetooth"},
	{"dlna", "DLNA / UPnP"},
	{"cast", "Google Cast"},
	{"qobuz", "Qobuz"},
	{"spotify", "Spotify"},
	{"tidal", "Tidal"},
	{"usb", "USB playback"},
}

// confHardware is the invariant hardware reference for the LP10 (the one model
// this tool targets), alphabetical by label, encoding the teardown's findings: a
// line-level streamer, no power amp, Cirrus/Wolfson WM8904 codec, optical S/PDIF
// up to 24-bit/192 kHz. The audio-chain and compute facts only — live memory/link
// usage is the resources/network cards' job, so nothing here repeats a live gauge.
var confHardware = []struct{ k, v string }{
	{"codec", "Cirrus/Wolfson WM8904 (DAC + ADC)"},
	{"line in", "3.5 mm aux → WM8904 ADC"},
	{"line out", "3.5 mm · 1 Vrms (no power amp)"},
	{"optical", "S/PDIF TOSLINK ≤ 24-bit/192 kHz"},
	{"radio", "dual-band 802.11ac · BT 5.0"},
	{"soc", "Amlogic A113L · 2× Cortex-A35"},
}

// serviceStrip renders the capability matrix (from ConfView) as dense grouped
// rows — "on  ● a ● b …" / "off ○ c ○ d …" — plus the env-gating note. A group
// that outgrows the column WRAPS onto aligned continuation rows (flowGroup)
// rather than clipping, so no service is ever hidden and the dots keep their
// colours at any width. Degrades to a "reading…" line until @@c arrives.
func (m *model) serviceStrip(w int) []string {
	cv := m.st.ConfView()
	if cv == nil {
		return []string{clipStyled(m.sty.sDmr.Render("reading from device…"), w)}
	}
	var on, off []string
	for _, sv := range confServices {
		if cv.Svc[sv.id] == "on" {
			on = append(on, m.sty.sAcc.Render("●")+" "+m.sty.sTxt.Render(sv.label))
		} else {
			off = append(off, m.sty.sDmr.Render("○")+" "+m.sty.sDim.Render(sv.label))
		}
	}
	rows := m.flowGroup("on", on, w)
	rows = append(rows, m.flowGroup("off", off, w)...)
	rows = append(rows, m.sty.sDmr.Render("env-gated · toggle in the Arylic app"))
	// Budget every row to w (visible cols) — after the wrap this only bites on a
	// single item wider than the whole column, or the note at a tiny width.
	for i, r := range rows {
		rows[i] = clipStyled(r, w)
	}
	return rows
}

// flowGroup flows one service group into rows at most w wide, separated by
// single spaces (the ● / ○ dots already separate the items visually). The
// 4-column group label heads the first row — "on  " / "off " keep the dots
// aligned across groups — and continuation rows indent to sit under the items.
func (m *model) flowGroup(label string, items []string, w int) []string {
	if len(items) == 0 {
		return nil
	}
	const indent = 4
	var out []string
	line, lineW := m.sty.sDim.Render(label)+strings.Repeat(" ", indent-len(label)), indent
	for _, it := range items {
		itW := lipgloss.Width(it)
		if lineW > indent && lineW+1+itW > w { // +1: the separating space
			out = append(out, line)
			line, lineW = strings.Repeat(" ", indent), indent
		}
		if lineW > indent {
			line, lineW = line+" ", lineW+1
		}
		line, lineW = line+it, lineW+itW
	}
	return append(out, line)
}

// ---- row primitives & formatters -----------------------------------------------

// fmtKHz renders a sample rate in kHz: "44.1 kHz", "48 kHz", "96 kHz".
func fmtKHz(hz int) string {
	if hz%1000 == 0 {
		return strconv.Itoa(hz/1000) + " kHz"
	}
	return strconv.FormatFloat(float64(hz)/1000, 'f', 1, 64) + " kHz"
}

// clipStyled clips an already-styled string to display width w, keeping the
// styling: ansi.Truncate cuts between escape sequences (measuring width the way
// lipgloss does, so it agrees with padVis) and every segment left of the cut
// keeps its colour. It used to strip-and-re-dim instead, which flattened a
// clipped services/eq row to a uniform grey the moment a larger font cost the
// column a couple of cells.
func clipStyled(styled string, w int) string {
	if lipgloss.Width(styled) <= w {
		return styled
	}
	if w <= 0 {
		return ""
	}
	if ell := GL["ell"]; w > DispW(ell) {
		return ansi.Truncate(styled, w, ell)
	}
	return ansi.Truncate(styled, w, "") // no room for the ellipsis: hard cut
}

// gridRow renders a two-column "label value | label value" row, exactly W wide.
func (m *model) gridRow(k1, v1, k2, v2 string, W int) string {
	half := W / 2
	return m.cellKV(k1, v1, half) + m.cellKV(k2, v2, W-half)
}

func (m *model) cellKV(k, v string, w int) string {
	const labW = 9
	vv := Clip(v, w-labW)
	out := m.sty.sDim.Render(k) + labelGap(k, labW) + m.sty.sTxt.Render(vv)
	if vis := labW + DispW(vv); vis < w {
		out += strings.Repeat(" ", w-vis)
	}
	return out
}

// diagLine renders "label  value" with a fixed dim label column.
func (m *model) diagLine(label, value string) string {
	return m.sty.sDim.Render(label) + labelGap(label, diagLabelW) + value
}

// diagGauge renders "label  [gauge]  value detail", clipping the dim detail to the
// body width w so a long detail (e.g. the cpu load triplet at a narrow terminal)
// can't size the row past the frame — the stacked counterpart to the cards cg()
// detail clip. Pass detail="" for a gauge with no trailing note.
func (m *model) diagGauge(label, gauge, value, detail string, w int) string {
	row := m.sty.sDim.Render(label) + labelGap(label, diagLabelW) + gauge + "  " + value
	if detail != "" {
		row += m.sty.sDmr.Render(Clip(detail, w-lipgloss.Width(row))) // Clip("",<=0)→""
	}
	return clipStyled(row, w) // never exceed the body width (a no-op when it fits)
}

func freqToChan(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz-2412)/5 + 1
	case mhz >= 5000:
		return (mhz - 5000) / 5
	}
	return 0
}

// fmtRate renders a bytes/sec throughput in the largest unit that keeps it ≥1.
func fmtRate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

// fmtLatencyMs renders a millisecond latency with one decimal under 10ms (sub-ms
// LAN hops would otherwise round to a meaningless "0"), whole numbers above.
// (Distinct from FmtMs(int), which formats a track position as MM:SS.)
func fmtLatencyMs(ms float64) string {
	if ms < 10 {
		return fmt.Sprintf("%.1f", ms)
	}
	return fmt.Sprintf("%.0f", ms)
}

// latencyRow renders one target — name, average, jitter, and the window peak
// (amber once a real spike has landed, so an intermittent glitch is visible
// after the fact). The fields are fixed-width so the columns line up across the
// three rows. Plain text on purpose: the earlier per-row sparkline rendered as
// ragged block glyphs on fonts whose block elements don't fill the cell.
func (m *model) latencyRow(name string, ps protocol.PingStat) string {
	t := m.sty
	return t.sDim.Render(padDisp(name, latNameW)) +
		t.sTxt.Render(rpadDisp(fmtLatencyMs(ps.Avg), latAvgW)+latAvgUnit) + " " +
		t.sDmr.Render(padDisp("±"+fmtLatencyMs(ps.Jitter), latJitW)) + " " +
		m.latencyPeakPen(ps).Render("max "+fmtLatencyMs(ps.Peak))
}

// pingLabel shortens the configured internet target for the latency row: an IP
// is shown whole, a hostname collapses to its second-level domain
// (apresolve.spotify.com → spotify).
func pingLabel(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "net"
	}
	parts := strings.Split(host, ".")
	if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return host // numeric final label → an IPv4 address; show it whole
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return host
}

func fmtUptime(up string) string {
	secs, err := strconv.ParseFloat(strings.TrimSpace(up), 64)
	if err != nil || secs < 0 {
		return "—"
	}
	s := int(secs)
	switch d, h, mn := s/86400, s%86400/3600, s%3600/60; {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, mn)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, mn)
	default:
		return fmt.Sprintf("%dm", mn)
	}
}

const (
	// diagLabelW is the dim label column shared by every diagnostics row (see
	// diagLine / diagGauge): the label, left-padded to this width, then the value.
	diagLabelW = 10

	// The latency row's fixed fields, in render order (see latencyRow).
	latNameW   = 8     // target name (left-padded)
	latAvgW    = 4     // average ms (right-aligned), before its unit
	latAvgUnit = " ms" // the avg field's trailing unit
	latJitW    = 5     // ±jitter
)

func orDash(s string) string { return cmp.Or(s, "—") }

func firstSeg(s string, sep byte) string {
	if before, _, ok := strings.Cut(s, string(sep)); ok {
		return before
	}
	return s
}

// labelGap is the space run after a fixed-width diagnostics label: the column
// width minus the label's display width, floored at 0 so a label wider than its
// column can never produce a negative (panicking) repeat count.
func labelGap(label string, col int) string {
	return strings.Repeat(" ", max(0, col-DispW(label)))
}
