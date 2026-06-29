package workers

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

const (
	tunnelDialTimeout = 3 * time.Second
	// tunnelSeedSpacing paces the on-connect queries: the device only answers a
	// query reliably when they aren't sent back-to-back in one burst.
	tunnelSeedSpacing = 150 * time.Millisecond
	tunnelPoll        = 250 * time.Millisecond // write-loop wake to re-check Stop
	tunnelCarryMax    = 8192                   // bound a separator-free read
	// TCP keepalive on the control socket. The device only broadcasts on change,
	// so the tunnel is normally silent — a read deadline would false-fire. OS
	// keepalive instead probes a half-open link (flaky-WiFi: device off the LAN,
	// local socket still ESTABLISHED) and tears it down in ~Idle+Count*Interval
	// (~25s) so eqConnected drops and we reconnect, instead of the ~10min default
	// while EQ writes vanish behind a false "connected" indicator.
	tunnelKeepIdle  = 10 * time.Second
	tunnelKeepIntvl = 5 * time.Second
	tunnelKeepCount = 3
)

// EQCommand is a queued control write for the :2018 tunnel: a wire code and a
// value (the caller clamps via tunnel.Clamp).
type EQCommand struct {
	Code string
	Val  int
}

// tunnelAddr resolves the control-tunnel address; LP10_TUNNEL_ADDR overrides it
// for tests (mirroring LP10_SSH for the ssh transport).
func tunnelAddr(cfg config.Config) string {
	if a := os.Getenv("LP10_TUNNEL_ADDR"); a != "" {
		return a
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(tunnel.Port))
}

// TunnelWorker maintains the device's plain-text control connection (:2018): it
// reconnects with backoff, seeds current values on connect, applies the device's
// broadcasts to State, and writes queued EQ commands. It never dies — a dead
// tunnel only disables the EQ panel; the ssh player stream is unaffected.
func TunnelWorker(st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand) {
	backoff := InitialBackoff
	for !st.Stop.IsSet() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.Note(fmt.Sprintf("tunnel worker: %v", r))
					st.Stop.Wait(time.Second)
				}
			}()
			backoff = tunnelOnce(st, cfg, eqcmds, backoff)
		}()
	}
}

// tunnelOnce is one connection lifecycle, returning the next reconnect backoff.
func tunnelOnce(st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand, backoff time.Duration) time.Duration {
	dialer := net.Dialer{
		Timeout: tunnelDialTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     tunnelKeepIdle,
			Interval: tunnelKeepIntvl,
			Count:    tunnelKeepCount,
		},
	}
	conn, err := dialer.Dial("tcp", tunnelAddr(cfg))
	if err != nil {
		st.SetEQConnected(false)
		return waitBackoff(st, backoff)
	}
	st.SetEQConnected(true)

	done := make(chan struct{})
	go tunnelReader(st, conn, done)

	// Seed: query each control so the panel shows the device's current values.
	for _, q := range tunnel.SeedQueries() {
		if st.Stop.IsSet() {
			break
		}
		if _, werr := conn.Write([]byte(q)); werr != nil {
			break
		}
		st.Stop.Wait(tunnelSeedSpacing)
	}

	// Write loop: drain queued commands until the connection dies or we stop. One
	// ticker (not a fresh time.After each iteration) gives the periodic wake that
	// lets us notice Stop with no commands in flight, without churning timers.
	dead := false
	poll := time.NewTicker(tunnelPoll)
	defer poll.Stop()
	for !st.Stop.IsSet() && !dead {
		select {
		case <-done:
			dead = true
		case cmd := <-eqcmds:
			if _, werr := conn.Write([]byte(tunnel.Set(cmd.Code, cmd.Val))); werr != nil {
				dead = true
			}
		case <-poll.C:
		}
	}

	conn.Close()
	<-done // reader exits once the closed conn fails its Read
	st.SetEQConnected(false)

	if st.Stop.IsSet() {
		return backoff
	}
	return waitBackoff(st, backoff)
}

// tunnelReader parses the device's broadcast frames into State until the
// connection fails (which the writer triggers on Stop by closing conn).
func tunnelReader(st *protocol.State, conn net.Conn, done chan struct{}) {
	defer close(done)
	defer func() { recover() }()
	buf := make([]byte, 4096)
	var carry string
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			updates, rest := tunnel.ParseFrames(carry + string(buf[:n]))
			if len(rest) > tunnelCarryMax {
				rest = "" // separator-free flood: drop, keep framing
			}
			carry = rest
			for _, u := range updates {
				st.ApplyTunnel(u.Code, u.Val)
			}
		}
		if err != nil {
			return
		}
	}
}

// waitBackoff sleeps the current backoff (interruptible by Stop) and returns the
// doubled-and-capped next value, mirroring streamOnce's cadence.
func waitBackoff(st *protocol.State, backoff time.Duration) time.Duration {
	if !st.Stop.Wait(backoff) {
		backoff *= 2
		if backoff > MaxBackoff {
			backoff = MaxBackoff
		}
	}
	return backoff
}
