package inventory

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// maxOUI bounds the vendor table. The IEEE registry holds about forty thousand
// registrations, so the cap is far above the real file and only stops a
// corrupted or hostile file from filling the router's memory.
const maxOUI = 100000

// LoadOUI reads the IEEE oui.txt file. Only the "(hex)" lines are registrations;
// the "(base 16)" lines repeat them, so counting both would double every vendor.
// The key is the six hex digits upper-cased, with no separators.
func LoadOUI(r io.Reader) (map[string]string, error) {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, "(hex)")
		if i < 0 {
			continue
		}
		prefix := strings.ReplaceAll(strings.TrimSpace(line[:i]), "-", "")
		if len(prefix) != 6 {
			continue
		}
		if len(m) >= maxOUI {
			return nil, fmt.Errorf("oui: more than %d registrations, refusing the file", maxOUI)
		}
		m[strings.ToUpper(prefix)] = strings.TrimSpace(line[i+len("(hex)"):])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m, nil
}
