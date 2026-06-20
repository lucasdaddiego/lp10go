// Package fixtures embeds the captured/synthetic LUCI wire records so both the
// fake ssh transport (cmd/fakessh) and the parsing tests share one source of
// truth. Mirrors lp10/tests/fixtures/.
package fixtures

import (
	"embed"
	"io/fs"
)

//go:embed *.txt
var files embed.FS

// Get returns the named fixture's text, panicking if it is missing — a typo in
// a test fixture name should fail loudly, not silently read "".
func Get(name string) string {
	b, err := fs.ReadFile(files, name)
	if err != nil {
		panic("fixtures: " + err.Error())
	}
	return string(b)
}
