package protocol

import (
	"iter"
	"strings"
)

// tags is the set of section letters a record may carry. 'i' is the one-shot
// static device/network info block and 'c' the one-shot capability/config block
// (both key=value lines); 'd' and 'g' are the one-shot raw register reads for
// the device-details (reg 92) and multiroom-group (reg 39) JSON. All four are
// sent once per connection.
var tags = map[byte]bool{'B': true, 'p': true, 't': true, 'v': true, 's': true, 'i': true, 'c': true, 'd': true, 'g': true}

const maxRecLines = 200 // a legitimate record is ~30 lines

// Record is one framed snapshot: section letter -> its lines.
type Record map[string][]string

// IterRecords turns a line source into a sequence of framed records. The 'B'
// key is present only when an @@B section appeared. Bad lines never panic. A
// single section that grows past maxRecLines (malformed flood) is shed on its
// own — the record's other, well-formed sections are kept; framing is kept.
// nextLine returns (line, true) per line and ("", false) at EOF.
func IterRecords(nextLine func() (string, bool)) iter.Seq[Record] {
	return func(yield func(Record) bool) {
		rec := Record{}
		key := byte(0)
		n := 0 // lines accumulated into the current section (reset per section)
		for {
			line, ok := nextLine()
			if !ok {
				return // EOF: drop any partial record (no @@E)
			}
			line = strings.TrimRight(line, "\n")
			if strings.HasPrefix(line, "@@") {
				var tag byte
				if len(line) >= 3 {
					tag = line[2]
				}
				switch {
				case tag == 'E':
					if !yield(rec) {
						return
					}
					rec, key, n = Record{}, 0, 0
				case tags[tag]:
					key, n = tag, 0
					if _, exists := rec[string(key)]; !exists {
						rec[string(key)] = []string{}
					}
				default:
					key = 0
				}
			} else if key != 0 && line != "" {
				if n >= maxRecLines {
					// this section is flooding: drop what it accumulated and stop
					// appending, but keep the record's other (well-formed) sections
					delete(rec, string(key))
					key = 0
				} else {
					n++
					rec[string(key)] = append(rec[string(key)], line)
				}
			}
		}
	}
}
