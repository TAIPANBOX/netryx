package inventory

import "encoding/json"

// maxClients bounds the clients kept from one hostapd reply. A radio serves a
// few dozen devices; anything past this is a reply that cannot be trusted.
const maxClients = 4096

// hostapdClients is the part of `ubus call hostapd.<iface> get_clients` that
// matters here. The frequency is reported once for the radio, not per client,
// so the band is derived from the top level and shared by every client.
type hostapdClients struct {
	Freq    int `json:"freq"`
	Clients map[string]struct {
		Signal int `json:"signal"`
	} `json:"clients"`
}

// ParseHostapdClients returns the Wi-Fi link of each client, keyed by lowercase
// colon MAC. A reply that does not parse gives an empty map: the inventory then
// shows the devices without a link instead of failing the whole table.
func ParseHostapdClients(iface string, js []byte) map[string]Link {
	out := map[string]Link{}
	var h hostapdClients
	if err := json.Unmarshal(js, &h); err != nil {
		return out
	}
	band := bandOf(h.Freq)
	for mac, c := range h.Clients {
		if len(out) >= maxClients {
			break
		}
		out[normMAC(mac)] = Link{Kind: "wifi", Iface: iface, Band: band, Signal: c.Signal}
	}
	return out
}

// bandOf maps a centre frequency in MHz to the band a person knows it by. A
// zero or missing frequency gives "" rather than guessing 2.4 GHz.
func bandOf(freq int) string {
	switch {
	case freq <= 0:
		return ""
	case freq < 3000:
		return "2.4 GHz"
	case freq < 5925:
		return "5 GHz"
	default:
		return "6 GHz"
	}
}
