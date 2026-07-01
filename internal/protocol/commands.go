// The device-command side of the protocol: the queued Command type, batch
// reduction, and the stdin write whitelist.

package protocol

import (
	"regexp"
	"slices"
	"strconv"
	"time"
)

var transportWords = map[string]bool{
	"PAUSE": true, "RESUME": true, "NEXT": true, "PREV": true,
}

var volRe = regexp.MustCompile(`^\d{1,3}$`)

// Command is a queued device command carrying its own enqueue time.
type Command struct {
	Mid  int
	Data string
	TS   time.Time
}

// ReduceCommands collapses a command list: last volume wins (at its own
// position), consecutive PAUSE/RESUME runs collapse to the final one, every
// NEXT/PREV is preserved, order stable.
func ReduceCommands(cmds []Command) []Command {
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		switch {
		case c.Mid == 64 || c.Mid == 90:
			// last value wins for volume (64) and the stats toggle (90):
			// drop any earlier command with the same mid, keep this one
			out = append(slices.DeleteFunc(out, func(cc Command) bool { return cc.Mid == c.Mid }), c)
		case c.Mid == 40 && (c.Data == "PAUSE" || c.Data == "RESUME") &&
			len(out) > 0 && out[len(out)-1].Mid == 40 &&
			(out[len(out)-1].Data == "PAUSE" || out[len(out)-1].Data == "RESUME"):
			out[len(out)-1] = c
		default:
			out = append(out, c)
		}
	}
	return out
}

// ValidatePayload whitelists what may be written to the device's stdin. MID 90
// is the diagnostics-stats toggle (1 = overlay open, send @@s; 0 = closed): it
// only ever flips a flag on the device, never reaches LUCI_local.
func ValidatePayload(mid int, data string) bool {
	switch mid {
	case 40:
		return transportWords[data]
	case 64:
		if !volRe.MatchString(data) {
			return false
		}
		n, _ := strconv.Atoi(data)
		return n <= 100
	case 90:
		return data == "0" || data == "1"
	}
	return false
}
