// Command fakessh is the fake SSH transport for integration tests, substituted
// for ssh via $LP10_SSH. Port of tests/fake_lp10.py.
//
// It speaks the v2 wire grammar on stdout, swallows ssh-style argv, and selects
// behavior from $LP10_FAKE_SCENARIO:
//
//	normal          playing record, then heartbeat cycles forever (~0.05 s)
//	silent          one valid record, then nothing (arms the watchdog)
//	dataless        framed-but-empty records forever (LUCI wedged from birth)
//	eof             one valid record, then clean exit (stream died)
//	garbage         noise interleaved with valid cycles (per-line resilience)
//	authfail        ssh-like auth failure: stderr 'Permission denied', exit 255
//	keychain-locked askpass marker on stderr, then auth failure, exit 255
//	heal            authfail for the first N invocations, then normal
//
// Received stdin lines are appended to $LP10_FAKE_CMDLOG (delivery assertions).
// Stdin EOF ends the stream loop, mirroring the real remote loop dying.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/fixtures"
	"github.com/lucasdaddiego/lp10go/internal/transport"
)

var (
	stdinClosed atomic.Bool
	closedCh    = make(chan struct{})
	closeOnce   sync.Once
)

func markClosed() { closeOnce.Do(func() { stdinClosed.Store(true); close(closedCh) }) }

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func emit(text string) {
	os.Stdout.WriteString(text)
	os.Stdout.Sync()
}

func authfail() {
	fmt.Fprintln(os.Stderr, "root@fake: Permission denied (publickey,password).")
	os.Exit(255)
}

func streamForever() {
	emit(fixtures.Get("device_record.txt")) // one-shot @@i device/network block
	emit(fixtures.Get("playing_record.txt"))
	for !stdinClosed.Load() {
		time.Sleep(50 * time.Millisecond)
		emit(fixtures.Get("heartbeat_record.txt"))
	}
}

func main() {
	scenario := getenv("LP10_FAKE_SCENARIO", "normal")
	cmdlog := os.Getenv("LP10_FAKE_CMDLOG")
	fakedir := getenv("LP10_FAKE_DIR", "/tmp")

	go func() {
		var f *os.File
		if cmdlog != "" {
			f, _ = os.OpenFile(cmdlog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		}
		r := bufio.NewReader(os.Stdin)
		for {
			line, err := r.ReadString('\n')
			if line != "" && f != nil {
				f.WriteString(line)
				f.Sync()
			}
			if err != nil {
				break
			}
		}
		if f != nil {
			f.Close()
		}
		markClosed()
	}()

	switch scenario {
	case "authfail":
		authfail()
	case "keychain-locked":
		fmt.Fprintln(os.Stderr, transport.MarkerLocked)
		authfail()
	case "heal":
		counter := filepath.Join(fakedir, "lp10-fake-heal-count")
		n := 0
		if b, err := os.ReadFile(counter); err == nil {
			n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		os.WriteFile(counter, []byte(strconv.Itoa(n+1)), 0o644)
		healAfter := 2
		if v, err := strconv.Atoi(os.Getenv("LP10_FAKE_HEAL_AFTER")); err == nil {
			healAfter = v
		}
		if n < healAfter {
			authfail()
		}
		streamForever()
	case "eof":
		emit(fixtures.Get("playing_record.txt"))
		os.Exit(0)
	case "silent":
		emit(fixtures.Get("playing_record.txt"))
		select {
		case <-closedCh:
		case <-time.After(300 * time.Second):
		}
	case "dataless":
		for !stdinClosed.Load() {
			emit(fixtures.Get("dataless_record.txt"))
			time.Sleep(50 * time.Millisecond)
		}
	case "garbage":
		emit("!!*** not a frame ***\n")
		emit(fixtures.Get("playing_record.txt"))
		emit("MID-Read:42 Data:null Length:4\nrandom noise\n")
		for !stdinClosed.Load() {
			time.Sleep(50 * time.Millisecond)
			emit("@@nonsense\n")
			emit(fixtures.Get("heartbeat_record.txt"))
		}
	default: // normal
		streamForever()
	}
}
