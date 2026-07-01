// Network readout for the diagnostics overlay: throughput rates from the
// cumulative byte counters, and rolling latency rings reduced to avg / jitter /
// peak.

package protocol

import (
	"strconv"
	"time"
)

// PingStat is one latency target's rolling readout in milliseconds: the average,
// the jitter (mean absolute successive difference), and the peak over the window.
// OK is false until a sample arrives. Peak covers only the window held while the
// overlay was open.
type PingStat struct {
	Avg, Jitter, Peak float64
	OK                bool
}

// NetStat is the computed network readout for the diagnostics overlay: live
// throughput (bytes/sec) over the active interface, interface error/drop
// deltas since the session's first sample, and latency to the laptop, the
// gateway, and the configured internet host.
type NetStat struct {
	RxRate, TxRate float64
	RatesOK        bool
	// error/drop counters as session deltas (current − the connection's first
	// sample); ErrsOK is false until the loop ships the counters.
	RxErrs, TxErrs, Drops int64
	ErrsOK                bool
	Ping                  [3]PingStat // 0 laptop (client), 1 gateway, 2 internet
}

// pingRingMax bounds each latency ring. The device samples latency on every 3rd
// stats tick while the overlay is open (~one sample per few seconds), so 30 holds
// roughly a one-to-few-minute window for the rolling average, jitter, and peak.
// Ticks that skip the ping send "-", which updateNet drops, so a sparser cadence
// just slows the window rather than flattening it.
const pingRingMax = 30

// updateNet folds one @@s sample into the throughput rates and latency rings.
// Caller holds st.mu. Rates need a prior sample with elapsed time and monotonic
// counters (an interface flap or reboot zeroes them — skip rather than spike).
func (st *State) updateNet(si *SysInfo, now time.Time) {
	rx, rxErr := strconv.ParseInt(si.RxBytes, 10, 64)
	tx, txErr := strconv.ParseInt(si.TxBytes, 10, 64)
	if rxErr == nil && txErr == nil {
		if !st.netPrevAt.IsZero() {
			if dt := now.Sub(st.netPrevAt).Seconds(); dt > 0 && rx >= st.netPrevRx && tx >= st.netPrevTx {
				st.netRxRate = float64(rx-st.netPrevRx) / dt
				st.netTxRate = float64(tx-st.netPrevTx) / dt
				st.netRatesOK = true
			}
		}
		st.netPrevRx, st.netPrevTx, st.netPrevAt = rx, tx, now
	}
	for i, s := range [3]string{si.PingClient, si.PingGw, si.PingNet} {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			st.pingRing[i] = append(st.pingRing[i], v)
			if len(st.pingRing[i]) > pingRingMax {
				st.pingRing[i] = st.pingRing[i][len(st.pingRing[i])-pingRingMax:]
			}
		}
	}
	if vals, ok := errCounters(si); ok {
		if !st.errsOK {
			st.errBase, st.errsOK = vals, true
		}
		for i, v := range vals {
			if v < st.errBase[i] { // counter reset (reboot/flap): re-baseline
				st.errBase[i] = v
			}
		}
		st.errCur = vals
	}
}

// errCounters parses the four cumulative error/drop counters from an @@s
// sample; ok is false when any is absent or non-numeric (an older loop, or a
// hardware path without the statistics files).
func errCounters(si *SysInfo) (out [4]int64, ok bool) {
	for i, s := range [4]string{si.RxErrs, si.TxErrs, si.RxDrop, si.TxDrop} {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = v
	}
	return out, true
}

// NetView returns the computed throughput + error + latency readout for the overlay.
func (st *State) NetView() NetStat {
	st.mu.Lock()
	defer st.mu.Unlock()
	ns := NetStat{RxRate: st.netRxRate, TxRate: st.netTxRate, RatesOK: st.netRatesOK}
	if st.errsOK {
		ns.RxErrs = st.errCur[0] - st.errBase[0]
		ns.TxErrs = st.errCur[1] - st.errBase[1]
		ns.Drops = (st.errCur[2] - st.errBase[2]) + (st.errCur[3] - st.errBase[3])
		ns.ErrsOK = true
	}
	for i := range st.pingRing {
		ns.Ping[i] = pingStat(st.pingRing[i])
	}
	return ns
}

// pingStat reduces a latency ring to an average and a jitter (the mean absolute
// difference between successive samples — RFC 3550-style packet delay variation).
func pingStat(r []float64) PingStat {
	if len(r) == 0 {
		return PingStat{}
	}
	ps := PingStat{OK: true, Peak: r[0]}
	var sum float64
	for _, v := range r {
		sum += v
		if v > ps.Peak {
			ps.Peak = v
		}
	}
	ps.Avg = sum / float64(len(r))
	if len(r) > 1 {
		var ds float64
		for i := 1; i < len(r); i++ {
			d := r[i] - r[i-1]
			if d < 0 {
				d = -d
			}
			ds += d
		}
		ps.Jitter = ds / float64(len(r)-1)
	}
	return ps
}
