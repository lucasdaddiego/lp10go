package protocol

import (
	"strings"
	"testing"
	"time"
)

// the probe-verified reg-92 wire shape (2026-07-01, fw AR241CE_9243.16.2)
const devDetailsWire = `MID-Read:92 Data:{"macaddress":{"bt":"aa:bb:cc:dd:ee:fe","eth0":"aa:bb:cc:dd:ee:ff","wlan0":"aa:bb:cc:dd:ee:fd"},"serialnumber":{"device_serialnumber":"RKARYLLP100000000000"},"versioninfo":{"devicefwversion":"AR241CE_9243.16.2","mcuversion":"16"}} Length:230`

func TestParseDevDetails(t *testing.T) {
	d := parseDevDetails([]string{devDetailsWire})
	if d == nil {
		t.Fatal("probe-shaped reg-92 payload should parse")
	}
	if d.Serial != "RKARYLLP100000000000" || d.BTMAC != "aa:bb:cc:dd:ee:fe" ||
		d.MCU != "16" || d.FW != "AR241CE_9243.16.2" {
		t.Errorf("parsed details = %+v", d)
	}

	// a partially matching object keeps what it carries
	p := parseDevDetails([]string{`MID-Read:92 Data:{"versioninfo":{"mcuversion":16}} Length:34`})
	if p == nil || p.MCU != "16" || p.Serial != "" {
		t.Errorf("partial details = %+v, want MCU-only (numeric coerced)", p)
	}

	for name, lines := range map[string][]string{
		"absent":       nil,
		"not a read":   {"garbage output"},
		"not JSON":     {"MID-Read:92 Data:oops Length:4"},
		"wrong shape":  {`MID-Read:92 Data:{"unrelated":1} Length:15`},
		"JSON array":   {`MID-Read:92 Data:[1,2] Length:5`},
		"empty object": {`MID-Read:92 Data:{} Length:2`},
	} {
		if got := parseDevDetails(lines); got != nil {
			t.Errorf("%s: parseDevDetails = %+v, want nil", name, got)
		}
	}
}

func TestParseMultiroom(t *testing.T) {
	if mr := parseMultiroom([]string{`MID-Read:39 Data:{"devices":[]} Length:14`}); mr == nil || mr.Devices != 0 {
		t.Errorf("solo group = %+v, want 0 devices", mr)
	}
	linked := `MID-Read:39 Data:{"devices":[{"n":"Kitchen"},{"n":"Studio"}]} Length:44`
	if mr := parseMultiroom([]string{linked}); mr == nil || mr.Devices != 2 {
		t.Errorf("linked group = %+v, want 2 devices", mr)
	}
	for name, lines := range map[string][]string{
		"absent":       nil,
		"not JSON":     {"MID-Read:39 Data:nope Length:4"},
		"no devices":   {`MID-Read:39 Data:{"other":1} Length:11`},
		"devices !arr": {`MID-Read:39 Data:{"devices":3} Length:13`},
	} {
		if got := parseMultiroom(lines); got != nil {
			t.Errorf("%s: parseMultiroom = %+v, want nil", name, got)
		}
	}
}

// The @@d/@@g sections route through ApplyRecord into their State views, and
// the @@i name key lands in DevInfo.
func TestApplyRecordRoutesDetailsGroupAndName(t *testing.T) {
	st := NewState()
	feed := "@@i\nnet=eth\nname=Living\n@@E\n@@d\n" + devDetailsWire + "\n@@E\n" +
		"@@g\nMID-Read:39 Data:{\"devices\":[]} Length:14\n@@E\n"
	lines := strings.Split(strings.TrimSuffix(feed, "\n"), "\n")
	i := 0
	next := func() (string, bool) {
		if i >= len(lines) {
			return "", false
		}
		i++
		return lines[i-1], true
	}
	for rec := range IterRecords(next) {
		ApplyRecord(st, rec)
	}
	if dt := st.DetailsView(); dt == nil || dt.Serial != "RKARYLLP100000000000" {
		t.Errorf("DetailsView = %+v", dt)
	}
	if mr := st.MultiroomView(); mr == nil || mr.Devices != 0 {
		t.Errorf("MultiroomView = %+v", mr)
	}
	if dev := st.DevInfoView(); dev == nil || dev.Name != "Living" {
		t.Errorf("DevInfoView.Name = %+v", dev)
	}
}

// Error/drop counters read as session deltas: the connection's first sample is
// the baseline, growth shows as a delta, a counter reset re-baselines, and a
// reconnect starts the session over.
func TestErrCounterSessionDeltas(t *testing.T) {
	st := NewState()
	t0 := time.Now()
	si := func(rxe, txe, rxd, txd string) *SysInfo {
		return &SysInfo{RxErrs: rxe, TxErrs: txe, RxDrop: rxd, TxDrop: txd}
	}
	st.updateNet(si("0", "0", "256", "0"), t0)
	if n := st.NetView(); !n.ErrsOK || n.Drops != 0 {
		t.Errorf("first sample = %+v, want ErrsOK with zero deltas (boot noise baselined)", n)
	}
	st.updateNet(si("1", "0", "259", "0"), t0.Add(time.Second))
	if n := st.NetView(); n.RxErrs != 1 || n.Drops != 3 {
		t.Errorf("growth = %+v, want rx 1 / drops 3", n)
	}
	st.updateNet(si("0", "0", "2", "0"), t0.Add(2*time.Second)) // reboot: counters shrank
	if n := st.NetView(); n.RxErrs != 0 || n.Drops != 0 {
		t.Errorf("counter reset = %+v, want re-baselined zeros", n)
	}
	st.StartProc(&Proc{})
	if n := st.NetView(); n.ErrsOK {
		t.Error("a reconnect should clear ErrsOK until the next sample")
	}
	// a sample with missing counters (older loop) never sets ErrsOK
	st.updateNet(si("", "", "", ""), t0.Add(3*time.Second))
	if n := st.NetView(); n.ErrsOK {
		t.Error("absent counters should leave ErrsOK false")
	}
}
