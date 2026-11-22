package alerts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const token = "123456:SECRET-token-value"

type fakeInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (f fakeInfo) Name() string       { return "telegram.token" }
func (f fakeInfo) Size() int64        { return int64(len(token)) }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func hermes(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func rootOnly(string) (fs.FileInfo, error) { return fakeInfo{mode: 0o600, uid: 0}, nil }

// D5, measured on the Flint 2 (hermes-agent 0.21.5-r25): the package installs
// its UCI config as /etc/config/hermes (its build copies hermes-agent.config
// there and its init loads `hermes`), not /etc/config/hermes-agent, so netryx
// never found the bot and said Hermes was not installed. With the file at
// that path, its bot is read.
func TestTheBotIsReadFromTheFileThePackageInstalls(t *testing.T) {
	if HermesConfig != "/etc/config/hermes" {
		t.Fatalf("Hermes's settings are read from %s; the package installs /etc/config/hermes", HermesConfig)
	}
	p := filepath.Join(t.TempDir(), filepath.Base(HermesConfig))
	if err := os.WriteFile(p, []byte(botConf), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, why := ReadBot(p, rootOnly); why != "" || b.Chat != "987654321" {
		t.Fatalf("the bot at %s: %+v %q", p, b, why)
	}
}

const botConf = `
config platform 'telegram'
	option enabled '1'
	option token_file '/etc/hermes-agent/telegram.token'
	option home_channel '987654321'
	list allow_user_id '987654321'
`

func sender(t *testing.T, url string) *Sender {
	t.Helper()
	return NewSender(SenderOptions{API: url, HTTP: &http.Client{Timeout: 2 * time.Second},
		ReadToken: func(string) (string, error) { return token, nil }, PerHour: 20})
}

// 13 Q1: only sendMessage, to the owner's chat; never getUpdates, which would
// take the owner's messages from Hermes.
func TestOnlySendMessageIsCalledWithTheOwnersChat(t *testing.T) {
	var mu sync.Mutex
	var paths, chats, texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		chats = append(chats, r.PostForm.Get("chat_id"))
		texts = append(texts, r.PostForm.Get("text"))
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	bot, why := ReadBot(hermes(t, botConf), rootOnly)
	if why != "" {
		t.Fatal(why)
	}
	if err := sender(t, srv.URL).Send(context.Background(), bot, "netryx: test"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "POST /bot"+token+"/sendMessage" || chats[0] != "987654321" || texts[0] != "netryx: test" {
		t.Fatalf("requests %v %v %v", paths, chats, texts)
	}
}

// Review Focus 4: whatever Telegram does, no error carries the token.
func TestAnErrorNeverCarriesTheToken(t *testing.T) {
	bot, _ := ReadBot(hermes(t, botConf), rootOnly)
	for name, h := range map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"429": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
		"hangup": func(w http.ResponseWriter, r *http.Request) {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		},
		"slow": func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) },
	} {
		srv := httptest.NewServer(h)
		err := sender(t, srv.URL).Send(context.Background(), bot, "x")
		srv.Close()
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), token) {
			t.Fatalf("%s: the error carries the token: %v", name, err)
		}
	}
	// A server that is not there at all.
	err := sender(t, "http://127.0.0.1:1").Send(context.Background(), bot, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unreachable: %v", err)
	}
}

// 13 Q1: a token anyone but root can read disables Telegram, with the reason,
// and the token is not read.
func TestATokenFileOthersCanReadDisablesTelegram(t *testing.T) {
	path := hermes(t, botConf)
	for name, st := range map[string]fakeInfo{
		"0644":     {mode: 0o644},
		"0640":     {mode: 0o640},
		"not root": {mode: 0o600, uid: 1000},
	} {
		_, why := ReadBot(path, func(string) (fs.FileInfo, error) { return st, nil })
		if why == "" {
			t.Fatalf("%s: Telegram stays on", name)
		}
	}
	_, why := ReadBot(path, func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist })
	if why == "" {
		t.Fatal("a missing token file must say so")
	}
}

func TestWithoutHermesAlertsStayInTheConsole(t *testing.T) {
	if _, why := ReadBot(filepath.Join(t.TempDir(), "absent"), rootOnly); why == "" {
		t.Fatal("no Hermes config, yet a bot")
	}
	off := strings.Replace(botConf, "option enabled '1'", "option enabled '0'", 1)
	if _, why := ReadBot(hermes(t, off), rootOnly); why == "" {
		t.Fatal("Telegram off in Hermes, yet a bot")
	}
}

func TestSeveralAllowedUsersNeedAHomeChannel(t *testing.T) {
	one := strings.Replace(botConf, "\toption home_channel '987654321'\n", "", 1)
	bot, why := ReadBot(hermes(t, one), rootOnly)
	if why != "" || bot.Chat != "987654321" {
		t.Fatalf("one allowed user: %+v %q; want that user's chat", bot, why)
	}
	two := one + "\tlist allow_user_id '111111111'\n"
	if _, why := ReadBot(hermes(t, two), rootOnly); !strings.Contains(why, "home_channel") {
		t.Fatalf("two allowed users and no home_channel: %q", why)
	}
}

func TestTelegramIsBoundedPerHour(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; w.Write([]byte(`{"ok":true}`)) }))
	defer srv.Close()
	bot, _ := ReadBot(hermes(t, botConf), rootOnly)
	s := NewSender(SenderOptions{API: srv.URL, HTTP: srv.Client(), ReadToken: func(string) (string, error) { return token, nil }, PerHour: 3,
		Now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }})
	var refused int
	for i := 0; i < 5; i++ {
		if err := s.Send(context.Background(), bot, "x"); err != nil {
			refused++
		}
	}
	if n != 3 || refused != 2 {
		t.Fatalf("sent %d, refused %d; want 3 and 2", n, refused)
	}
}

func TestEachAlertReadsAsOneLine(t *testing.T) {
	name := func(mac string) string { return map[string]string{cam: "Camera"}[mac] }
	for _, c := range []struct {
		a    Alert
		want string
	}{
		{Alert{Kind: NewDevice, MAC: cam}, "New device on the network: Camera"},
		{Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"}, "Connection attempt to blocked country RU: Camera"},
		{Alert{Kind: Upload, MAC: cam, Bytes: 500e6}, "Unusually large upload: Camera, 500 MB in an hour"},
		{Alert{Kind: NewDevice, MAC: "02:00:00:00:00:09"}, "New device on the network: 02:00:00:00:00:09"},
		{Alert{Kind: QuarantineOff}, "Quarantine is off: the list of known devices cannot be read"},
	} {
		got := Text(c.a, name)
		if !strings.HasPrefix(got, "netryx: ") || !strings.Contains(got, c.want) || strings.Contains(got, "\n") {
			t.Errorf("Text(%+v) = %q; want one line with %q", c.a, got, c.want)
		}
	}
}

// Review Focus 4: a failed send leaves the alert in the console with the
// reason, tried again after ten minutes, three times at most; the console
// channel marks it shown at once.
func TestAlertsAreDeliveredOrMarkedWithWhy(t *testing.T) {
	s, c, _ := newStore(t)
	s.Raise(Alert{Kind: NewDevice, MAC: cam})
	bot := Bot{TokenFile: "/x", Chat: "1"}
	calls := 0
	fail := func(context.Context, Bot, string) error {
		calls++
		return errors.New("Telegram: answer 429")
	}
	for i := 0; i < 5; i++ {
		s.Deliver(context.Background(), "telegram", bot, "", fail, nil)
		c.t = c.t.Add(11 * time.Minute)
	}
	if calls != MaxTries {
		t.Fatalf("tried %d times; want %d", calls, MaxTries)
	}
	if a := s.List()[0]; a.Sent != "" || a.Problem != "Telegram: answer 429" {
		t.Fatalf("after failures: %+v", a)
	}
	s.Raise(Alert{Kind: NewDevice, MAC: "02:00:00:00:00:01"})
	s.Deliver(context.Background(), "telegram", Bot{}, "Telegram is off in Hermes", fail, nil)
	if a := s.List()[0]; a.Sent != "" || a.Problem != "Telegram is off in Hermes" {
		t.Fatalf("without a bot: %+v", a)
	}
	s.Raise(Alert{Kind: NewDevice, MAC: "02:00:00:00:00:02"})
	s.Deliver(context.Background(), "console", bot, "", fail, nil)
	if a := s.List()[0]; a.Sent != "console" {
		t.Fatalf("console channel: %+v", a)
	}
	ok := 0
	s.Raise(Alert{Kind: NewDevice, MAC: "02:00:00:00:00:03"})
	s.Deliver(context.Background(), "telegram", bot, "", func(_ context.Context, _ Bot, text string) error {
		ok++
		if !strings.HasPrefix(text, "netryx: ") {
			t.Errorf("text %q", text)
		}
		return nil
	}, nil)
	if a := s.List()[0]; a.Sent != "telegram" || ok != 1 {
		t.Fatalf("a delivered alert: %+v (sent %d)", a, ok)
	}
}

// A flood of new devices must not use up the tries of what matters more: a
// local refusal (the hour's budget) is not a try, and the more serious kinds
// go first (automated security review of 9a490ea).
func TestAFloodNeverBuriesTheSeriousAlerts(t *testing.T) {
	s, _, _ := newStore(t)
	for i := 0; i < 3; i++ {
		s.Raise(Alert{Kind: NewDevice, MAC: fmt.Sprintf("02:00:00:00:00:%02x", i)})
	}
	s.Raise(Alert{Kind: Upload, MAC: cam, Bytes: 500e6})
	s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"})
	var order []string
	budget := 0
	send := func(_ context.Context, _ Bot, text string) error {
		if budget == 0 {
			return ErrTooMany
		}
		budget--
		order = append(order, text)
		return nil
	}
	for i := 0; i < 5; i++ {
		s.Deliver(context.Background(), "telegram", Bot{Chat: "1"}, "", send, nil)
	}
	budget = 2
	s.Deliver(context.Background(), "telegram", Bot{Chat: "1"}, "", send, nil)
	if len(order) != 2 || !strings.Contains(order[0], "upload") || !strings.Contains(order[1], "blocked country") {
		t.Fatalf("sent %v; want the upload, then the country try, despite five refusals", order)
	}
	budget = 10
	s.Deliver(context.Background(), "telegram", Bot{Chat: "1"}, "", send, nil)
	if len(s.Unsent()) != 0 {
		t.Fatalf("still unsent after the budget came back: %+v", s.Unsent())
	}
}

// Final review C1: without a bot every unsent alert was marked again every
// 30 s and the file rewritten each time; a router without Hermes wore its
// flash out. The same reason marks once.
func TestAnUnchangedReasonIsNotWrittenAgain(t *testing.T) {
	s, _, _ := newStore(t)
	for i := 0; i < 200; i++ {
		s.Raise(Alert{Kind: NewDevice, MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)})
	}
	nosend := func(context.Context, Bot, string) error { t.Fatal("no bot, no send"); return nil }
	s.Deliver(context.Background(), "telegram", Bot{}, "Hermes is not installed", nosend, nil)
	before := s.writes
	s.Deliver(context.Background(), "telegram", Bot{}, "Hermes is not installed", nosend, nil)
	if n := s.writes - before; n != 0 {
		t.Fatalf("a second round with the same reason wrote the file %d times", n)
	}
	s.Mark(s.List()[0].ID, "", "Hermes is not installed")
	if s.writes != before {
		t.Fatal("a Mark that changes nothing wrote the file")
	}
}
