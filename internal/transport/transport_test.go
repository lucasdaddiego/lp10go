package transport

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/config"
)

// withRunSecurity swaps the injectable security(1) runner for one test.
func withRunSecurity(t *testing.T, fn func() secOutcome) {
	t.Helper()
	orig := runSecurity
	runSecurity = fn
	t.Cleanup(func() { runSecurity = orig })
}

func TestKeychainPasswordSuccess(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{stdout: "hunter2\n"} })
	pw, err := KeychainPassword()
	if err != nil || pw != "hunter2" {
		t.Fatalf("KeychainPassword = (%q, %v), want (hunter2, nil)", pw, err)
	}
}

func TestKeychainPasswordNoItem(t *testing.T) {
	withRunSecurity(t, func() secOutcome {
		return secOutcome{rc: 44, stderr: "security: ... could not be found."}
	})
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerNoItem) {
		t.Fatalf("err = %v, want %s", err, MarkerNoItem)
	}
}

func TestKeychainPasswordOtherFailureMeansLocked(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{rc: 51, stderr: "some other error"} })
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerLocked) {
		t.Fatalf("err = %v, want %s", err, MarkerLocked)
	}
}

func TestKeychainPasswordTimeoutMeansLocked(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{timeout: true} })
	_, err := KeychainPassword()
	te, ok := err.(*TransportError)
	if !ok || !strings.Contains(te.Msg, MarkerLocked) || te.Cadence != 60*time.Second {
		t.Fatalf("err = %v, want locked w/ 60s cadence", err)
	}
}

func TestKeychainPasswordOSErrorIsMarkedNotRaisedRaw(t *testing.T) {
	withRunSecurity(t, func() secOutcome {
		return secOutcome{runErr: &exec.Error{Name: "security", Err: exec.ErrNotFound}}
	})
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerBroken) {
		t.Fatalf("err = %v, want %s", err, MarkerBroken)
	}
	terr := ClassifyStderr(err.Error())
	if terr == nil || !terr.Fatal || terr.Cadence != 60*time.Second {
		t.Fatalf("classify = %v, want fatal w/ 60s cadence", terr)
	}
}

func TestAskpassFailureRoundtripsToFatalClass(t *testing.T) {
	// marker-drift guard: whatever the askpass markers are, classify_stderr
	// must map them to a fatal class.
	for _, marker := range []string{MarkerNoItem, MarkerLocked, MarkerBroken} {
		terr := ClassifyStderr(marker)
		if terr == nil || !terr.Fatal {
			t.Errorf("classify(%q) = %v, want fatal", marker, terr)
		}
	}
}

func TestKeychainHintKeepsPasswordOutOfHistory(t *testing.T) {
	if strings.Contains(KeychainHint, "<password>") {
		t.Error("hint must not embed a literal password")
	}
	if !strings.HasSuffix(strings.TrimRight(KeychainHint, " "), "-w") {
		t.Error("hint must end with -w so security(1) prompts")
	}
}

func TestRemoteLoopIsValidShellAndWhitelistsMids(t *testing.T) {
	body := RemoteLoop("")
	if r := exec.Command("sh", "-n", "-c", body).Run(); r != nil {
		t.Fatalf("remote loop is not valid shell: %v", r)
	}
	if !strings.Contains(body, `case "$mid" in 40|64)`) {
		t.Error("missing command whitelist")
	}
	if !strings.Contains(body, "read -r -t 0 || break") {
		t.Error("missing two-step burst drain idiom")
	}
	if strings.Contains(body, "eval") {
		t.Error("remote loop must not contain eval")
	}
	for _, tag := range []string{"@@B", "@@p", "@@t", "@@v", "@@s", "@@E"} {
		if !strings.Contains(body, tag) {
			t.Errorf("missing wire tag %q", tag)
		}
	}
}

func TestRemoteLoopCustomMidsAreInterpolated(t *testing.T) {
	if !strings.Contains(RemoteLoop("40"), `case "$mid" in 40)`) {
		t.Error("custom mids should be interpolated")
	}
}

// TestRemoteLoopParsesDeviceOutput pulls the actual device-output parse snippets
// out of the loop body and runs them under sh against representative iw/ip-route/
// fwVersion samples, so a future edit that breaks the shell parameter-expansion
// parsing fails here rather than silently on the device (which CI can't run).
func TestRemoteLoopParsesDeviceOutput(t *testing.T) {
	body := RemoteLoop("")
	extractCase := func(marker string) string {
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("parse snippet %q not found in remote loop", marker)
		}
		j := strings.Index(body[i:], "esac")
		if j < 0 {
			t.Fatalf("no esac after %q", marker)
		}
		return body[i : i+j+len("esac")]
	}
	const nlDef = "nl=$(printf '\\nx'); nl=${nl%x}; " // newline sentinel, as the loop builds it
	cases := []struct{ name, setup, marker, result, want string }{
		{"gateway", "ir='default via 192.168.1.1 dev wlan0\n192.168.1.0/24 dev wlan0'; gw=",
			`case "$ir" in *"default via "*`, "gw", "192.168.1.1"},
		{"ssid with spaces", "wl='Connected\n\tSSID: MyWiFi 5G\n\tfreq: 5180'; ss=",
			`case "$wl" in *"SSID: "*`, "ss", "MyWiFi 5G"},
		{"freq", "wl='\tSSID: x\n\tfreq: 5180\n\tsignal: -43 dBm'; fq=",
			`case "$wl" in *"freq: "*`, "fq", "5180"},
		{"rate", "wl='\tfreq: 5180\n\ttx bitrate: 780.0 MBit/s\n\tbss flags'; rt=",
			`case "$wl" in *"tx bitrate: "*`, "rt", "780.0"},
		{"fwVersion line", `ln='build_date = "2025-12-24"'; bd=; ap=; pf=`,
			`case "$ln" in *build_date`, "bd", "2025-12-24"},
	}
	for _, c := range cases {
		script := nlDef + c.setup + "; " + extractCase(c.marker) + `; printf '%s' "$` + c.result + `"`
		out, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Errorf("%s: sh error: %v", c.name, err)
			continue
		}
		if string(out) != c.want {
			t.Errorf("%s: parsed %q, want %q", c.name, string(out), c.want)
		}
	}
}

// TestRemoteLoopStructuralContract guards invariants the Go parser and the
// on-demand-stats protocol depend on: the positional @@s field order, the
// diag-gated stat block, the MID-90 toggle, the playback-only side-effects, and
// the early-break stat scans.
func TestRemoteLoopStructuralContract(t *testing.T) {
	body := RemoteLoop("")
	for _, want := range []string{
		`echo "$up $la $lb $lc $ma $mt $nc $fw.$fv $kt-$kr ${tp:--} ${sl:--} ${lq:--} ${ry:--}"`,
		`if [ "$dg" = 1 ]; then`,
		`90) case "$data" in 1) dg=1;; *) dg=0;; esac;;`,
		`[ $pc = 1 ] && { i=0; bw=4; idl=0; pc49=0; }`,
		`MemAvailable:) ma=$v; break;;`,
		// position poll gated to every 3rd tick, with the read-flag that keeps the
		// track-skip detector working across skip ticks
		`pc49=$((pc49-1)); if [ $idl -lt 5 ] && [ $pc49 -le 0 ]; then`,
		`if [ $rd -eq 1 ]; then case "$pn" in`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("remote loop missing structural invariant:\n  %s", want)
		}
	}
}

func TestSSHArgvContract(t *testing.T) {
	t.Setenv("LP10_SSH", "")
	cfg := config.Config{Host: "192.168.1.40", User: "root", Name: "LP10 · Living", VolStep: 2}
	argv := SSHArgv(cfg)
	flat := strings.Join(argv, " ")
	if argv[0] != "ssh" {
		t.Errorf("argv[0] = %q, want ssh", argv[0])
	}
	for _, want := range []string{
		"-F /dev/null", "-T", "UserKnownHostsFile=/dev/null",
		"StrictHostKeyChecking=no", "ConnectTimeout=3",
		"ServerAliveInterval=20", "NumberOfPasswordPrompts=1",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("argv missing %q", want)
		}
	}
	target := cfg.User + "@" + cfg.Host
	found := false
	for _, a := range argv {
		if a == target {
			found = true
		}
	}
	if !found {
		t.Errorf("argv missing target %q", target)
	}
	t.Setenv("LP10_SSH", "/tmp/fake-ssh")
	if SSHArgv(cfg)[0] != "/tmp/fake-ssh" {
		t.Error("LP10_SSH should override the ssh binary")
	}
}

func TestClassifyStderr(t *testing.T) {
	err := ClassifyStderr("root@x: Permission denied (publickey,password).")
	if err == nil || !err.Fatal || err.Cadence != 10*time.Second {
		t.Errorf("permission-denied: %v", err)
	}
	err = ClassifyStderr("lp10-askpass: keychain-locked\nroot@x: Permission denied.")
	if err == nil || !err.Fatal || err.Cadence != 60*time.Second {
		t.Errorf("locked: %v", err)
	}
	err = ClassifyStderr("lp10-askpass: no-item\nroot@x: Permission denied.")
	if err == nil || !err.Fatal || !strings.Contains(err.Error(), "add-generic-password") {
		t.Errorf("no-item: %v", err)
	}
	if ClassifyStderr("ssh: connect to host x: Operation timed out") != nil {
		t.Error("transient network failure should classify as nil")
	}
	if ClassifyStderr("") != nil {
		t.Error("empty stderr should classify as nil")
	}
}
