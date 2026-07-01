package protocol

import (
	"os"
	"strings"
	"testing"
)

// TestSysStatsFieldOrder cross-checks the @@s positional schema against the
// device loop's emitter: the sf* indices in apply.go and the `echo "$up ..."`
// payload line in transport's remote_loop.src.sh must change in lockstep.
// Count first, then a few positional anchors so a reorder (not just an
// append/drop) also fails here rather than silently on the device.
func TestSysStatsFieldOrder(t *testing.T) {
	src, err := os.ReadFile("../transport/remote_loop.src.sh")
	if err != nil {
		t.Fatalf("read remote_loop.src.sh: %v", err)
	}
	var payload string
	for line := range strings.SplitSeq(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `echo "$up `) {
			payload = line
			break
		}
	}
	if payload == "" {
		t.Fatal(`remote_loop.src.sh: @@s payload line (echo "$up ...) not found`)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(payload, `echo "`), `";`)
	fields := strings.Fields(inner)
	if len(fields) != sysFieldCount {
		t.Fatalf("remote_loop.src.sh @@s emits %d fields; parseSysInfo expects %d — update the sf* indices and the emitter together",
			len(fields), sysFieldCount)
	}
	anchors := map[int]string{
		sfUp:       "$up",
		sfAvail:    "$ma",
		sfFW:       "$fw.$fv",
		sfOS:       "$kt-$kr",
		sfTempmC:   "${tp:--}",
		sfPingNet:  "$pnt",
		sfDacRate:  "${ar:--}",
		sfCpuKHz:   "${cf:--}",
		sfProcs:    "${r1:--}",
		sfNoiseDBm: "${ns:--}",
		sfRxErrs:   "${rxe:--}",
		sfTxDrop:   "${txd:--}",
	}
	for idx, want := range anchors {
		if fields[idx] != want {
			t.Errorf("@@s field %d = %q, protocol expects the %q column here", idx, fields[idx], want)
		}
	}
}
