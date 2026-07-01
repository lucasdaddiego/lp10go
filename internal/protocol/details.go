// Once-per-connection register payloads beyond the now-playing record: the
// device-details JSON (reg 92, the @@d section) and the multiroom group
// (reg 39, @@g), shipped raw by the device loop and parsed here.

package protocol

import (
	"regexp"
	"strings"
)

// reMBData extracts the Data payload of any LUCI register read.
var reMBData = regexp.MustCompile(`(?s)MID-Read:\d+ Data:(.*) Length:\d+\s*$`)

// regJSON decodes the JSON Data payload of a joined register-read section,
// returning nil when the section is absent, isn't a register read, or isn't
// JSON.
func regJSON(lines []string) any {
	if len(lines) == 0 {
		return nil
	}
	m := reMBData.FindStringSubmatch(strings.Join(lines, "\n"))
	if m == nil {
		return nil
	}
	return parseJSON(m[1])
}

// DevDetails is the once-per-connection identity readout from reg 92
// (DEVICE_DETAILS): the serial number, the Bluetooth MAC, the MCU firmware
// version, and the full firmware string — which carries the trailing
// sub-version (AR241CE_9243.16.2) that the reg-5/6 pair drops.
type DevDetails struct {
	Serial, BTMAC, MCU, FW string
}

// parseDevDetails parses the @@d section. The wire shape (probe-verified
// 2026-07-01 against fw AR241CE_9243.16.2):
//
//	{"macaddress":{"bt":…,"eth0":…,"wlan0":…},
//	 "serialnumber":{"device_serialnumber":…},
//	 "versioninfo":{"devicefwversion":…,"mcuversion":…}}
//
// Nil when the section is absent or nothing recognisable parses; a partially
// matching object keeps whatever fields it does carry.
func parseDevDetails(lines []string) *DevDetails {
	obj, _ := regJSON(lines).(map[string]any)
	if obj == nil {
		return nil
	}
	str := func(section, key string) string {
		sec, _ := obj[section].(map[string]any)
		if sec == nil {
			return ""
		}
		v, present := sec[key]
		if !present || v == nil {
			return ""
		}
		return printable(pyStr(v))
	}
	d := &DevDetails{
		Serial: str("serialnumber", "device_serialnumber"),
		BTMAC:  str("macaddress", "bt"),
		MCU:    str("versioninfo", "mcuversion"),
		FW:     str("versioninfo", "devicefwversion"),
	}
	if *d == (DevDetails{}) {
		return nil
	}
	return d
}

// Multiroom is the once-per-connection multiroom-group readout from reg 39:
// the number of linked devices (0 = solo).
type Multiroom struct {
	Devices int
}

// parseMultiroom parses the @@g section ({"devices":[…]}); nil when absent or
// unrecognisable.
func parseMultiroom(lines []string) *Multiroom {
	obj, _ := regJSON(lines).(map[string]any)
	if obj == nil {
		return nil
	}
	devs, ok := obj["devices"].([]any)
	if !ok {
		return nil
	}
	return &Multiroom{Devices: len(devs)}
}
