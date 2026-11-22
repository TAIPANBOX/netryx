// Package uciconf reads UCI config files and writes `uci batch` text.
package uciconf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Section is one `config` block. A single `option` is a one-element slice;
// each `list` line appends.
type Section struct {
	Type, Name string
	Options    map[string][]string
}

// Change is one edit to the netryx package for Batch.
//
// Delete removes the section and nothing else. Replace deletes the section
// first, so it ends up exactly as Set gives it. Without Replace the change
// merges: options not named in Set are kept, and an empty slice for a key
// deletes that option.
type Change struct {
	Section, Type string
	Set           map[string][]string
	Delete        bool
	Replace       bool
}

const maxValue = 128

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Parse reads a UCI file. An anonymous section is named @<type>[<n>], n being
// its index among the sections of that type, as UCI itself counts them.
func Parse(r io.Reader) ([]Section, error) {
	var secs []Section
	perType := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		start := n
		line := sc.Text()
		words, err := split(line)
		// A backslash that ends a line outside single quotes continues the
		// statement on the next line, as libuci reads it.
		for errors.Is(err, errContinue) {
			if !sc.Scan() {
				err = fmt.Errorf("backslash at the end of the file")
				break
			}
			n++
			line = line[:len(line)-1] + sc.Text()
			words, err = split(line)
		}
		if err != nil {
			return nil, fmt.Errorf("uci: line %d: %v", start, err)
		}
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "package":
			// libuci reads it (uci export writes it): it names the file.
			continue
		case "config":
			if len(words) < 2 || len(words) > 3 {
				return nil, fmt.Errorf("uci: line %d: config needs a type and an optional name", n)
			}
			s := Section{Type: words[1], Options: map[string][]string{}}
			if len(words) == 3 {
				s.Name = words[2]
			} else {
				s.Name = fmt.Sprintf("@%s[%d]", s.Type, perType[s.Type])
			}
			perType[s.Type]++
			secs = append(secs, s)
		case "option", "list":
			if len(secs) == 0 {
				return nil, fmt.Errorf("uci: line %d: %s before any config line", n, words[0])
			}
			if len(words) != 3 {
				return nil, fmt.Errorf("uci: line %d: %s needs a name and a value", n, words[0])
			}
			s := &secs[len(secs)-1]
			if words[0] == "option" {
				s.Options[words[1]] = []string{words[2]}
			} else {
				s.Options[words[1]] = append(s.Options[words[1]], words[2])
			}
		default:
			return nil, fmt.Errorf("uci: line %d: unknown keyword %q", n, words[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("uci: line %d: %v", n+1, err)
	}
	return secs, nil
}

// errContinue says the line ends in a backslash that joins the next one.
var errContinue = errors.New("continued")

// split breaks a line into words the way libuci's parser does: unquoted,
// 'single' or "double" quoted, adjacent pieces joined; # outside quotes starts
// a comment. A backslash outside single quotes takes the next character as it
// is, which is how uci writes an apostrophe ('it'\”s') and how a double
// quote stands inside double quotes ("a\"b"); inside single quotes it is an
// ordinary character. A backslash at the end of the line is errContinue.
func split(line string) ([]string, error) {
	var words []string
	i := 0
	for i < len(line) {
		c := line[i]
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if c == '#' {
			break
		}
		var w strings.Builder
		for i < len(line) {
			c = line[i]
			if c == ' ' || c == '\t' || c == '\r' {
				break
			}
			switch c {
			case '\\':
				if i+1 >= len(line) {
					return nil, errContinue
				}
				w.WriteByte(line[i+1])
				i += 2
			case '\'':
				end := strings.IndexByte(line[i+1:], c)
				if end < 0 {
					return nil, fmt.Errorf("unterminated quote")
				}
				w.WriteString(line[i+1 : i+1+end])
				i += end + 2
			case '"':
				i++
				for {
					if i >= len(line) {
						return nil, fmt.Errorf("unterminated quote")
					}
					if line[i] == '"' {
						i++
						break
					}
					if line[i] == '\\' {
						if i+1 >= len(line) {
							return nil, errContinue
						}
						i++
					}
					w.WriteByte(line[i])
					i++
				}
			default:
				w.WriteByte(c)
				i++
			}
		}
		words = append(words, w.String())
	}
	return words, nil
}

func badValue(v string) error {
	if len(v) > maxValue {
		return fmt.Errorf("value longer than %d bytes", maxValue)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("value is not valid UTF-8")
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == '\'' {
			return fmt.Errorf("value contains %q", r)
		}
	}
	return nil
}

// Batch returns the text for `uci -q batch` on stdin. Values are single
// quoted, which is safe because a value holding a quote is refused. A list
// option is deleted before its add_list lines so a rewrite replaces it.
func Batch(changes []Change) (string, error) {
	var b strings.Builder
	for _, c := range changes {
		if !nameRE.MatchString(c.Section) {
			return "", fmt.Errorf("uci: bad section name %q", c.Section)
		}
		if c.Delete {
			fmt.Fprintf(&b, "delete netryx.%s\n", c.Section)
			continue
		}
		if !nameRE.MatchString(c.Type) {
			return "", fmt.Errorf("uci: bad section type %q", c.Type)
		}
		if c.Replace {
			fmt.Fprintf(&b, "delete netryx.%s\n", c.Section)
		}
		fmt.Fprintf(&b, "set netryx.%s=%s\n", c.Section, c.Type)
		keys := make([]string, 0, len(c.Set))
		for k := range c.Set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !nameRE.MatchString(k) {
				return "", fmt.Errorf("uci: bad option name %q", k)
			}
			vals := c.Set[k]
			for _, v := range vals {
				if err := badValue(v); err != nil {
					return "", fmt.Errorf("uci: option %s: %v", k, err)
				}
			}
			if len(vals) == 0 {
				if !c.Replace {
					fmt.Fprintf(&b, "delete netryx.%s.%s\n", c.Section, k)
				}
				continue
			}
			if len(vals) == 1 {
				fmt.Fprintf(&b, "set netryx.%s.%s='%s'\n", c.Section, k, vals[0])
				continue
			}
			fmt.Fprintf(&b, "delete netryx.%s.%s\n", c.Section, k)
			for _, v := range vals {
				fmt.Fprintf(&b, "add_list netryx.%s.%s='%s'\n", c.Section, k, v)
			}
		}
	}
	b.WriteString("commit netryx\n")
	return b.String(), nil
}
