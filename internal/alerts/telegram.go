package alerts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"netryx/internal/uciconf"
)

// Bot is Hermes's Telegram bot as netryx may use it (13 Q1): the file Hermes
// keeps the token in, and the owner's chat.
type Bot struct{ TokenFile, Chat string }

// HermesConfig is where Hermes keeps its settings (docs/stage2c-measured.md,
// section 5): the hermes-agent package installs its config as
// /etc/config/hermes and its init loads `hermes`.
const HermesConfig = "/etc/config/hermes"

// ReadBot reads Hermes's telegram section. It answers a reason instead of a
// bot when Hermes or its Telegram is not there, when the token file is missing
// or anyone but root could read it (the token is then not read at all), or
// when no single owner's chat can be chosen.
func ReadBot(uciPath string, stat func(string) (fs.FileInfo, error)) (Bot, string) {
	f, err := os.Open(uciPath)
	if err != nil {
		return Bot{}, "Hermes is not installed: alerts stay in the console"
	}
	defer f.Close()
	secs, err := uciconf.Parse(f)
	if err != nil {
		return Bot{}, "Hermes settings could not be read: alerts stay in the console"
	}
	var tg *uciconf.Section
	for i := range secs {
		if secs[i].Name == "telegram" {
			tg = &secs[i]
		}
	}
	if tg == nil || !on(get(tg, "enabled")) {
		return Bot{}, "Telegram is off in Hermes: alerts stay in the console"
	}
	b := Bot{TokenFile: get(tg, "token_file")}
	if b.TokenFile == "" {
		b.TokenFile = "/etc/hermes-agent/telegram.token"
	}
	fi, err := stat(b.TokenFile)
	if err != nil {
		return Bot{}, "the bot token file is missing: alerts stay in the console"
	}
	if fi.Mode().Perm()&0o077 != 0 || !ownedByRoot(fi) {
		return Bot{}, "users other than root can read the bot token (" + b.TokenFile + "): Telegram is off"
	}
	b.Chat = get(tg, "home_channel")
	if b.Chat == "" {
		ids := tg.Options["allow_user_id"]
		switch len(ids) {
		case 0:
			return Bot{}, "Hermes has no owner chat: alerts stay in the console"
		case 1:
			b.Chat = strings.TrimSpace(ids[0])
		default:
			return Bot{}, "the bot has several users: set home_channel in Hermes so alerts reach the owner"
		}
	}
	if !chatID(b.Chat) {
		return Bot{}, "the owner chat in Hermes does not look like a chat id"
	}
	return b, ""
}

func get(s *uciconf.Section, k string) string {
	if v := s.Options[k]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

func on(v string) bool {
	switch strings.ToLower(v) {
	case "1", "on", "yes", "true", "enabled":
		return true
	}
	return false
}

func ownedByRoot(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

// chatID is a Telegram chat id: digits, maybe negative (a group).
func chatID(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SenderOptions: API is Telegram's address (a field for tests, never a
// config option, so no setting can send the token elsewhere); ReadToken reads
// the token file only when a message goes out; PerHour bounds the messages.
type SenderOptions struct {
	API       string
	HTTP      *http.Client
	ReadToken func(path string) (string, error)
	Now       func() time.Time
	PerHour   int
}

// TelegramAPI is the only address the sender talks to.
const TelegramAPI = "https://api.telegram.org"

// sendTimeout bounds one message.
const sendTimeout = 10 * time.Second

// Sender calls Telegram's sendMessage and nothing else.
type Sender struct {
	o    SenderOptions
	mu   sync.Mutex
	sent []time.Time // within the last hour
}

// NewSender fills the defaults: Telegram's API, a client with the send
// deadline, a token read from its file, 20 messages an hour.
func NewSender(o SenderOptions) *Sender {
	if o.API == "" {
		o.API = TelegramAPI
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: sendTimeout}
	}
	if o.ReadToken == nil {
		o.ReadToken = func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return strings.TrimSpace(string(b)), err
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PerHour <= 0 {
		o.PerHour = 20
	}
	return &Sender{o: o}
}

// ErrTooMany says the hour's messages are used up.
var ErrTooMany = errors.New("Telegram: too many messages this hour")

// Send posts text to the bot's chat. Any error is rewritten so it carries no
// address and no token.
func (s *Sender) Send(ctx context.Context, b Bot, text string) error {
	if !s.take() {
		return ErrTooMany
	}
	tok, err := s.o.ReadToken(b.TokenFile)
	if err != nil || tok == "" || strings.ContainsAny(tok, "/?#\r\n ") {
		return errors.New("Telegram: the bot token could not be read")
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	form := url.Values{"chat_id": {b.Chat}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.API+"/bot"+tok+"/sendMessage", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return errors.New("Telegram: the request could not be built")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram: %s", kind(err))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram: answer %d", resp.StatusCode)
	}
	return nil
}

// kind names a transport error without the URL it carries.
func kind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "no answer in time"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	default:
		return "connection failed"
	}
}

func (s *Sender) take() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	kept := s.sent[:0]
	for _, t := range s.sent {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	s.sent = kept
	if len(s.sent) >= s.o.PerHour {
		return false
	}
	s.sent = append(s.sent, now)
	return true
}

// Text is one line of English for an alert; name gives a device's name, ""
// when it has none (the MAC is shown then).
func Text(a Alert, name func(mac string) string) string {
	who := a.MAC
	if name != nil {
		if n := strings.Join(strings.Fields(name(a.MAC)), " "); n != "" {
			who = n
		}
	}
	var t string
	switch a.Kind {
	case NewDevice:
		t = "New device on the network: " + who
	case BlockedCountry:
		t = "Connection attempt to blocked country " + a.Country + ": " + who
	case Upload:
		t = fmt.Sprintf("Unusually large upload: %s, %d MB in an hour", who, a.Bytes/1e6)
	case QuarantineFull:
		t = "Too many new devices: the quarantine list is full, new devices still cannot reach the house"
	case NewDevices:
		t = fmt.Sprintf("Many new devices at once: %d, none can reach the house", a.Count)
	case QuarantineOff:
		t = "Quarantine is off: the list of known devices cannot be read, new devices can reach the house"
	case BaselineRebuilt:
		t = "The list of known devices was damaged and rebuilt; for 24 hours new devices join it without quarantine"
	default:
		t = a.Kind
	}
	return "netryx: " + t
}
