package api

import (
	"net/http"
	"strings"
	"testing"

	"netryx/internal/uciconf"
)

// A person is a name and the devices that are theirs, kept in its own UCI
// section and written only when the owner changes it.
func TestAPersonIsWrittenAndReadBack(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPut, "/api/people/sam", `{"name":"Sam","macs":["AA-BB-CC-DD-EE-02","aa:bb:cc:dd:ee:03","aa:bb:cc:dd:ee:03"]}`)
	if rec.Code != http.StatusOK || len(fr.writes) != 1 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	got := batchOf(t, fr.writes[0])
	want := "delete netryx.sam\nset netryx.sam=person\ndelete netryx.sam.mac\nadd_list netryx.sam.mac='aa:bb:cc:dd:ee:02'\nadd_list netryx.sam.mac='aa:bb:cc:dd:ee:03'\nset netryx.sam.name='Sam'\ncommit netryx\n"
	if got != want {
		t.Errorf("batch:\n%s\nwant:\n%s", got, want)
	}
	fr.secs = applyChanges(fr.secs, fr.writes[0])
	list, _ := decode(t, get(t, h, http.MethodGet, "/api/people")).([]any)
	if len(list) != 1 || field(list[0], "id") != "sam" || field(list[0], "name") != "Sam" || len(arr(field(list[0], "macs"))) != 2 {
		t.Fatalf("people: %v", list)
	}
	for _, b := range []string{`{"name":"","macs":[]}`, `{"name":"X","macs":["not a mac"]}`, `{"name":"X\u0007","macs":[]}`} {
		if rec := send(t, h, http.MethodPut, "/api/people/x", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", b, rec.Code)
		}
	}
	for _, id := range []string{"Sam", "agent_x", "kids", "r_night"} {
		if rec := send(t, h, http.MethodPut, "/api/people/"+id, `{"name":"X","macs":[]}`); rec.Code == http.StatusOK {
			t.Errorf("person id %q was taken", id)
		}
	}
}

// One device belongs to at most one person: giving it to a second is refused
// and names the first, so nothing moves without the owner seeing it.
func TestADeviceBelongsToOnePerson(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs, sec("person", "sam", "name", "Sam", "mac", phone))
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPut, "/api/people/alex", `{"name":"Alex","macs":["`+phone+`"]}`)
	if rec.Code != http.StatusBadRequest || len(fr.writes) != 0 || !strings.Contains(errorOf(t, rec), "Sam") {
		t.Errorf("a device already Sam's: %d %s", rec.Code, rec.Body)
	}
	// Sam keeps it when Sam's own section is written again.
	if rec := send(t, h, http.MethodPut, "/api/people/sam", `{"name":"Sam B","macs":["`+phone+`"]}`); rec.Code != http.StatusOK {
		t.Errorf("rewriting Sam: %d %s", rec.Code, rec.Body)
	}
	// A config written by hand with the device twice gives it to the first.
	fr2 := house()
	fr2.secs = append(fr2.secs, sec("person", "sam", "name", "Sam", "mac", phone), sec("person", "alex", "name", "Alex", "mac", phone))
	h2, _ := writable(t, fr2)
	list, _ := decode(t, get(t, h2, http.MethodGet, "/api/people")).([]any)
	if len(list) != 2 || len(arr(field(list[0], "macs"))) != 1 || len(arr(field(list[1], "macs"))) != 0 {
		t.Errorf("the device twice by hand: %v", list)
	}
}

// Deleting a person removes only the person: the devices, their names and
// groups, and every rule stay as they were.
func TestDeletingAPersonLeavesDevicesAndRules(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs, sec("person", "sam", "name", "Sam", "mac", phone))
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodDelete, "/api/people/sam", "")
	if rec.Code != http.StatusOK || len(fr.writes) != 1 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if got := batchOf(t, fr.writes[0]); got != "delete netryx.sam\ncommit netryx\n" {
		t.Errorf("batch:\n%s", got)
	}
	if rec := send(t, h, http.MethodDelete, "/api/people/kids", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a group is not a person: %d", rec.Code)
	}
}

// A person written through the console is in the committed config, so a
// restart of netryx (or of the router) reads it back.
func TestAPersonSurvivesARestart(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPut, "/api/people/sam", `{"name":"Sam","macs":["`+phone+`"]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if !strings.HasSuffix(batchOf(t, fr.writes[0]), "commit netryx\n") {
		t.Fatalf("the person is not committed")
	}
	// A new process reads the config the batch left.
	text, err := uciconf.Batch(fr.writes[0])
	if err != nil || text == "" {
		t.Fatalf("batch: %v", err)
	}
	after := &fakeRules{secs: applyChanges(house().secs, fr.writes[0])}
	h2, _ := writable(t, after)
	list, _ := decode(t, get(t, h2, http.MethodGet, "/api/people")).([]any)
	if len(list) != 1 || field(list[0], "name") != "Sam" || str(at(field(list[0], "macs"), 0)) != phone {
		t.Errorf("after a restart: %v", list)
	}
}
