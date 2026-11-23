// Package askagent puts a question about a stretch of traffic to the Hermes
// agent on the same router, by the contract in hermes-openwrt's
// docs/netryx-ask.md: POST http://127.0.0.1:<port>/v1/ask with the key from
// Hermes's root-only file, a body of question and context only, and an answer
// or a refusal whose message is written to be shown as it is.
//
// The key is read from its file for every question and goes nowhere but the
// Authorization header: never into an error, a log or the page.
package askagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"netryx/internal/uciconf"
)

const (
	// HermesConfig is where Hermes's package installs its settings.
	HermesConfig = "/etc/config/hermes"
	// DefaultKeyFile and DefaultPort are Hermes's defaults for hermes.api.
	DefaultKeyFile = "/etc/hermes-agent/api.key"
	DefaultPort    = 8642
	// MaxQuestion and MaxContext are characters, MaxBody bytes, as the
	// contract takes them.
	MaxQuestion = 2000
	MaxContext  = 8000
	MaxBody     = 16384
	// Timeout is the request's own: the agent has 110 s.
	Timeout = 120 * time.Second
	// maxAnswer bounds what is read back.
	maxAnswer = 256 << 10
)

// NotEnabled is what the page says when the agent cannot be asked.
const NotEnabled = "agent not enabled on this router"

// Refusal is an answer other than 200, or a question that could not be put:
// Message is for the person, as the agent wrote it where it wrote one.
type Refusal struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int
}

func (r *Refusal) Error() string { return r.Message }

// Answer is the agent's 200.
type Answer struct {
	Answer     string   `json:"answer"`
	CostUSD    *float64 `json:"cost_usd"`
	AsksLeft   *int     `json:"asks_left_this_hour"`
	BudgetLeft *float64 `json:"budget_left_usd"`
}

// Client asks the agent; Config is Hermes's UCI file (HermesConfig when empty).
type Client struct {
	Config string
	HTTP   *http.Client
}

type conf struct {
	port    int
	keyFile string
}

func notEnabled(why string) *Refusal {
	msg := NotEnabled
	if why != "" {
		msg += ": " + why
	}
	return &Refusal{Code: "not_enabled", Message: msg}
}

// settings reads hermes.api: on only with enabled '1', the port and key file
// as Hermes takes them.
func (c *Client) settings() (conf, *Refusal) {
	path := c.Config
	if path == "" {
		path = HermesConfig
	}
	f, err := os.Open(path)
	if err != nil {
		return conf{}, notEnabled("Hermes is not installed")
	}
	defer f.Close()
	secs, err := uciconf.Parse(f)
	if err != nil {
		return conf{}, notEnabled("Hermes's settings cannot be read")
	}
	for _, s := range secs {
		if s.Name != "api" {
			continue
		}
		one := func(k string) string {
			if v := s.Options[k]; len(v) > 0 {
				return v[len(v)-1]
			}
			return ""
		}
		if one("enabled") != "1" {
			break
		}
		cf := conf{port: DefaultPort, keyFile: DefaultKeyFile}
		if p := one("port"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1024 || n > 65535 {
				return conf{}, notEnabled("hermes.api.port is not a port")
			}
			cf.port = n
		}
		if k := one("key_file"); k != "" {
			cf.keyFile = k
		}
		return cf, nil
	}
	return conf{}, notEnabled("questions are off in Hermes (hermes.api.enabled)")
}

// readKey reads the key as Hermes leaves it: a plain file, not a link, that
// nobody but its owner may read, of at least 32 characters.
func readKey(path string) (string, *Refusal) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", notEnabled("its key cannot be read")
	}
	if !fi.Mode().IsRegular() {
		return "", notEnabled("its key file is not a plain file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", notEnabled("its key file can be read by others than root")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && st.Uid != 0 {
		return "", notEnabled("its key file is not root's")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 4096 {
		return "", notEnabled("its key cannot be read")
	}
	k := strings.TrimSpace(string(b))
	if len(k) < 32 || strings.ContainsAny(k, " \t\r\n") {
		return "", notEnabled("its key is not a key")
	}
	return k, nil
}

// Available reports whether the agent can be asked: questions on in Hermes
// and a key netryx can read. The agent itself is asked only by Ask.
func (c *Client) Available() (bool, string) {
	cf, ref := c.settings()
	if ref == nil {
		_, ref = readKey(cf.keyFile)
	}
	if ref != nil {
		return false, ref.Message
	}
	return true, ""
}

// cut returns s cut to at most n characters and at most b bytes, never inside one.
func cut(s string, n, b int) string {
	if utf8.RuneCountInString(s) > n {
		i := 0
		for j := range s {
			if i == n {
				s = s[:j]
				break
			}
			i++
		}
	}
	for len(s) > b {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// Ask puts the question, with the context cut to what the contract takes.
func (c *Client) Ask(ctx context.Context, question, contextText string) (Answer, error) {
	q := strings.TrimSpace(question)
	if q == "" || utf8.RuneCountInString(q) > MaxQuestion || !utf8.ValidString(q) {
		return Answer{}, &Refusal{Status: 400, Code: "bad_request", Message: fmt.Sprintf("A question is 1 to %d characters.", MaxQuestion)}
	}
	cf, ref := c.settings()
	if ref != nil {
		return Answer{}, ref
	}
	key, ref := readKey(cf.keyFile)
	if ref != nil {
		return Answer{}, ref
	}
	in := map[string]string{"question": q}
	if contextText = strings.TrimSpace(contextText); contextText != "" {
		// What is left of the body for the context, with room for the JSON around it.
		qb, _ := json.Marshal(q)
		room := MaxBody - len(qb) - 64
		ctxText := cut(strings.ToValidUTF8(contextText, ""), MaxContext, room)
		for {
			body, _ := json.Marshal(map[string]string{"question": q, "context": ctxText})
			if len(body) <= MaxBody || ctxText == "" {
				break
			}
			ctxText = cut(ctxText, MaxContext, len(ctxText)-(len(body)-MaxBody))
		}
		if ctxText != "" {
			in["context"] = ctxText
		}
	}
	body, err := json.Marshal(in)
	if err != nil || len(body) > MaxBody {
		return Answer{}, &Refusal{Status: 413, Code: "too_large", Message: "The question is too long to send."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(cf.port)+"/v1/ask", bytes.NewReader(body))
	if err != nil {
		return Answer{}, &Refusal{Code: "unreachable", Message: "The router's agent cannot be asked."}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: Timeout}
	}
	res, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return Answer{}, notEnabled("it is not running, or its questions are off")
		}
		var ne interface{ Timeout() bool }
		if errors.As(err, &ne) && ne.Timeout() {
			return Answer{}, &Refusal{Status: 504, Code: "timeout", Message: "The router's agent did not answer within two minutes."}
		}
		return Answer{}, &Refusal{Code: "unreachable", Message: "The router's agent did not answer."}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if res.StatusCode == http.StatusOK {
		var a Answer
		if json.Unmarshal(raw, &a) != nil || strings.TrimSpace(a.Answer) == "" {
			return Answer{}, &Refusal{Status: 502, Code: "agent_failed", Message: "The router's agent answered something that is not an answer."}
		}
		return a, nil
	}
	var e struct {
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			RetryAfter int    `json:"retry_after"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Code == "" || e.Error.Message == "" {
		return Answer{}, &Refusal{Status: res.StatusCode, Code: "agent_failed", Message: fmt.Sprintf("The router's agent could not answer (HTTP %d).", res.StatusCode)}
	}
	retry := e.Error.RetryAfter
	if retry == 0 {
		retry, _ = strconv.Atoi(res.Header.Get("Retry-After"))
	}
	return Answer{}, &Refusal{Status: res.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RetryAfter: retry}
}
