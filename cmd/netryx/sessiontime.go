package main

import (
	"bytes"
	"io/fs"
	"strconv"
	"time"

	"netryx/internal/uciconf"
)

// defaultSessionTime is LuCI's own default: an hour.
const defaultSessionTime = time.Hour

// luciSessionTime is how long LuCI's sessions last (luci.sauth.sessiontime,
// seconds), so the console asks rpcd for the same: rpcd's default of five
// minutes would sign out anyone who stepped away. An hour when it is not
// set, or not between five minutes and a day.
func luciSessionTime(fsys fs.FS) time.Duration {
	b, err := fs.ReadFile(fsys, "etc/config/luci")
	if err != nil {
		return defaultSessionTime
	}
	secs, err := uciconf.Parse(bytes.NewReader(b))
	if err != nil {
		return defaultSessionTime
	}
	for _, s := range secs {
		if s.Name != "sauth" || len(s.Options["sessiontime"]) == 0 {
			continue
		}
		n, err := strconv.Atoi(s.Options["sessiontime"][0])
		if err != nil || n < 300 || n > 86400 {
			return defaultSessionTime
		}
		return time.Duration(n) * time.Second
	}
	return defaultSessionTime
}
