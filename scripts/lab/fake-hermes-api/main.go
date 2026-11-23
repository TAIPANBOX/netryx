// fake-hermes-api answers as Hermes's question API does, by the contract in
// hermes-openwrt docs/netryx-ask.md, for trying netryx's "Ask the router's
// agent" in the lab without a model. Lab only: it is never packaged.
//
//	fake-hermes-api -key /etc/hermes-agent/api.key -addr 127.0.0.1:8642
//
// A question starting "err:<code>" gets that refusal (bad_request, busy,
// too_large, hourly_limit, daily_budget, agent_failed, usage_unreadable,
// timeout, not_found); any other question is answered with what the context
// said about the stretch, so the summary netryx sent can be seen.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

var refusals = map[string]struct {
	status int
	msg    string
	retry  int
}{
	"bad_request":      {400, "The question is not one the agent takes.", 0},
	"not_found":        {404, "Only POST /v1/ask is served.", 0},
	"busy":             {409, "Another question is being answered; ask again in a minute.", 60},
	"too_large":        {413, "The question and its context are too long.", 0},
	"hourly_limit":     {429, "The console may ask 10 questions an hour, and this hour's are used. The next one can be asked in 12 minutes.", 701},
	"daily_budget":     {429, "Today's budget for questions is spent. Questions can be asked again after midnight UTC.", 3600},
	"agent_failed":     {502, "The agent could not answer: the model refused its key.", 0},
	"usage_unreadable": {503, "The record of what questions cost cannot be read.", 0},
	"timeout":          {504, "The agent did not answer within 110 seconds.", 0},
}

func main() {
	keyFile := flag.String("key", "/etc/hermes-agent/api.key", "the key file")
	addr := flag.String("addr", "127.0.0.1:8642", "where to listen")
	flag.Parse()
	var mu sync.Mutex
	asked := 0
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		refuse := func(code string) {
			f := refusals[code]
			if f.retry > 0 {
				w.Header().Set("Retry-After", fmt.Sprint(f.retry))
			}
			w.WriteHeader(f.status)
			e := map[string]any{"code": code, "message": f.msg}
			if f.retry > 0 {
				e["retry_after"] = f.retry
			}
			json.NewEncoder(w).Encode(map[string]any{"error": e})
		}
		if r.URL.Path != "/v1/ask" {
			refuse("not_found")
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			io.WriteString(w, `{"error":{"code":"method_not_allowed","message":"Only POST."}}`)
			return
		}
		key, _ := os.ReadFile(*keyFile)
		if r.Header.Get("Authorization") != "Bearer "+strings.TrimSpace(string(key)) {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"code":"unauthorized","message":"The key is not the router's."}}`)
			return
		}
		if r.ContentLength > 16384 {
			refuse("too_large")
			return
		}
		var in map[string]string
		if err := json.NewDecoder(io.LimitReader(r.Body, 16385)).Decode(&in); err != nil || in["question"] == "" || len(in) > 2 {
			refuse("bad_request")
			return
		}
		if c, ok := strings.CutPrefix(in["question"], "err:"); ok {
			if _, known := refusals[c]; known {
				refuse(c)
				return
			}
		}
		mu.Lock()
		asked++
		left := 10 - asked
		mu.Unlock()
		lines := strings.Split(strings.TrimSpace(in["context"]), "\n")
		answer := fmt.Sprintf("(lab stand-in, no model) You asked: %q. The router's summary had %d lines; it began: %s", in["question"], len(lines), lines[0])
		json.NewEncoder(w).Encode(map[string]any{"answer": answer, "cost_usd": 0.0031, "asks_left_this_hour": left, "budget_left_usd": 0.4969})
		log.Printf("asked %q with %d bytes of context", in["question"], len(in["context"]))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
