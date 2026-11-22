package main

import (
	"testing"

	"netryx/internal/collect"
	"netryx/internal/flows"
)

// The collector names the router's own addresses with a key, and the flow
// table leaves that key out of the live ring. If the two spellings drifted
// apart the router would become a client in live view.
func TestTheFlowTableAndTheCollectorNameTheRouterAlike(t *testing.T) {
	if flows.RouterKey != collect.RouterKey {
		t.Fatalf("flows calls the router %q, the collector %q", flows.RouterKey, collect.RouterKey)
	}
}
