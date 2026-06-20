package workers

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/protocol"
)

// TestTunnelWorkerRoundTrip stands up a fake :2018 server and checks the worker
// applies device broadcasts to State and writes queued commands as CODE:VAL;.
func TestTunnelWorkerRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())

	st := protocol.NewState()
	eqcmds := make(chan EQCommand, 8)
	go TunnelWorker(st, config.Config{Host: "unused"}, eqcmds)
	defer st.Stop.Set()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	// The device broadcasts current values -> State reflects them, link is live.
	if _, err := conn.Write([]byte("MXV:42;BAS:-6;")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "MXV applied", func() bool {
		v, ok := st.EQValue("MXV")
		return ok && v == 42
	})
	if v, ok := st.EQValue("BAS"); !ok || v != -6 {
		t.Errorf("BAS=%d,%v want -6", v, ok)
	}
	if connected, _ := st.EQView(); !connected {
		t.Error("tunnel not marked connected")
	}

	// A queued command is written to the device, clamped, as CODE:VAL;.
	eqcmds <- EQCommand{Code: "BAS", Val: 99} // clamps to +10
	got := readUntilContains(t, conn, "BAS:10;")
	if !strings.Contains(got, "BAS:10;") {
		t.Errorf("server received %q, want it to contain BAS:10;", got)
	}
}

// TestTunnelWorkerEchoHold checks a locally-set value isn't clobbered by the
// device's broadcast echo arriving within the hold window.
func TestTunnelWorkerEchoHold(t *testing.T) {
	st := protocol.NewState()
	st.SetEQLocal("MXV", 30)  // user just set 30, hold armed
	st.ApplyTunnel("MXV", 99) // stale echo within hold -> ignored
	if v, _ := st.EQValue("MXV"); v != 30 {
		t.Errorf("MXV=%d want 30 (echo should be held off)", v)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func readUntilContains(t *testing.T, conn net.Conn, want string) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var buf []byte
	tmp := make([]byte, 256)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if strings.Contains(string(buf), want) {
				return string(buf)
			}
		}
		if err != nil {
			return string(buf)
		}
	}
}
