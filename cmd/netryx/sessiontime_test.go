package main

import (
	"testing"
	"testing/fstest"
	"time"
)

// The console's sessions last as long as LuCI's: luci.sauth.sessiontime, an
// hour when it is not set or not a sane number.
func TestTheSessionsLastAsLongAsLuCIs(t *testing.T) {
	luci := func(body string) fstest.MapFS {
		return fstest.MapFS{"etc/config/luci": {Data: []byte("config core 'main'\n\toption lang 'auto'\n\n" + body)}}
	}
	for _, c := range []struct {
		fs   fstest.MapFS
		want time.Duration
	}{
		{luci("config internal 'sauth'\n\toption sessionpath '/tmp/luci-sessions'\n\toption sessiontime '7200'\n"), 2 * time.Hour},
		{luci("config internal 'sauth'\n\toption sessionpath '/tmp/luci-sessions'\n"), time.Hour},
		{luci("config internal 'sauth'\n\toption sessiontime 'forever'\n"), time.Hour},
		{luci("config internal 'sauth'\n\toption sessiontime '5'\n"), time.Hour},
		{luci("config internal 'sauth'\n\toption sessiontime '9999999'\n"), time.Hour},
		{fstest.MapFS{}, time.Hour},
	} {
		if got := luciSessionTime(c.fs); got != c.want {
			t.Errorf("%v: got %v, want %v", c.fs, got, c.want)
		}
	}
}
