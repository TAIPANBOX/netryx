package api

// The JSON shapes the console reads. Field names are the wire contract, so
// they are spelled out here instead of derived from the domain types.

type appJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type bytesJSON struct {
	Down uint64 `json:"down"`
	Up   uint64 `json:"up"`
}

type rateJSON struct {
	Up   float64 `json:"up"`
	Down float64 `json:"down"`
}

// seriesPoint is one step of a traffic series. Down and Up are keyed by the
// split value; an unsplit series has the single key "total".
type seriesPoint struct {
	T string `json:"t"`
	// StepSec is how many seconds the point covers. A series may mix steps
	// (hours before a restart, minutes after), so a rate is taken over this,
	// never over the range's usual step.
	StepSec float64           `json:"stepSec"`
	Down    map[string]uint64 `json:"down"`
	Up      map[string]uint64 `json:"up"`
}

type wanJSON struct {
	Up     bool   `json:"up"`
	Device string `json:"device"`
	IPv4   string `json:"ipv4"`
	IPv6   string `json:"ipv6"`
	// UptimeSec is seconds, so the console does not parse a duration string.
	UptimeSec float64 `json:"uptimeSec"`
	RxRate    float64 `json:"rxRate"`
	TxRate    float64 `json:"txRate"`
	// Latency and jitter are null when no probe replied: a 0 ms reading on a
	// dead line would look like a fast one.
	LatencyMs *float64 `json:"latencyMs"`
	JitterMs  *float64 `json:"jitterMs"`
	LossPct   float64  `json:"lossPct"`
}

type clientsJSON struct {
	Online int `json:"online"`
	// New24h is null until the service has run for 24 h: before that every
	// device it meets would look new.
	New24h *int `json:"new24h"`
}

type topClientJSON struct {
	MAC       string   `json:"mac"`
	Name      string   `json:"name"`
	Down      uint64   `json:"down"`
	Up        uint64   `json:"up"`
	TopApp    *appJSON `json:"topApp"`
	Countries []string `json:"countries"`
	Other     bool     `json:"other"`
}

type topAppJSON struct {
	appJSON
	Clients int    `json:"clients"`
	Down    uint64 `json:"down"`
	Up      uint64 `json:"up"`
	Other   bool   `json:"other"`
}

type countryJSON struct {
	CC    string  `json:"cc"`
	Org   string  `json:"org"`
	Down  uint64  `json:"down"`
	Up    uint64  `json:"up"`
	Share float64 `json:"share"`
	Other bool    `json:"other"`
}

type overviewJSON struct {
	WAN        wanJSON         `json:"wan"`
	Clients    clientsJSON     `json:"clients"`
	Series     []seriesPoint   `json:"series"`
	TopClients []topClientJSON `json:"topClients"`
	TopApps    []topAppJSON    `json:"topApps"`
	Countries  []countryJSON   `json:"countries"`
	LiveFlows  int             `json:"liveFlows"`
	// LostEvents is how many connection events the kernel could not deliver
	// since the service started: bytes of those flows come only from dumps.
	LostEvents uint64 `json:"lostEvents"`
}

type linkJSON struct {
	Kind   string `json:"kind"`
	Iface  string `json:"iface"`
	SSID   string `json:"ssid"`
	Band   string `json:"band"`
	Signal int    `json:"signal"`
}

type deviceJSON struct {
	MAC      string   `json:"mac"`
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	Vendor   string   `json:"vendor"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Random   bool     `json:"random"`
	IPv4     []string `json:"ipv4"`
	IPv6     []string `json:"ipv6"`
	Link     linkJSON `json:"link"`
	First    string   `json:"first"`
	Last     string   `json:"last"`
	Online   bool     `json:"online"`
	Rate     rateJSON `json:"rate"`
	// Recent is the average over the window asked for (bytes per second) and
	// RecentSec the seconds that window covers.
	Recent    rateJSON  `json:"recent"`
	RecentSec float64   `json:"recentSec"`
	Last24h   bytesJSON `json:"last24h"`
	TopApp    *appJSON  `json:"topApp"`
	// CountryCount is how many countries the device talked to in 24 h.
	CountryCount int `json:"countryCount"`
	// Isolate and Quarantined are the device's isolation (spec 6.7) and
	// quarantine (6.8); Isolation says how it is kept from the house and
	// what is not covered; LikelySame is a hint from its own hostname.
	Isolate     bool           `json:"isolate"`
	Quarantined bool           `json:"quarantined"`
	LikelySame  string         `json:"likelySame,omitempty"`
	Isolation   *isolationJSON `json:"isolation,omitempty"`
}

type isolationJSON struct {
	Routed         bool   `json:"routed"`
	Bridged        bool   `json:"bridged"`
	SSID           string `json:"ssid"`
	SSIDIsolated   bool   `json:"ssidIsolated"`
	HardwareSwitch bool   `json:"hardwareSwitch"`
	Text           string `json:"text"`
}

type domainJSON struct {
	Name string `json:"name"`
	// Src is how the domain was last named: "sni", "quic", "dns", "ech" or
	// "none".
	Src   string `json:"src"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type placeJSON struct {
	CC string `json:"cc"`
	// Org is the organisation of the AS the app reached in that country.
	Org   string `json:"org"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type appUsageJSON struct {
	App     appJSON      `json:"app"`
	Down    uint64       `json:"down"`
	Up      uint64       `json:"up"`
	Flows   uint64       `json:"flows"`
	Share   float64      `json:"share"`
	Domains []domainJSON `json:"domains"`
	Places  []placeJSON  `json:"places"`
	Other   bool         `json:"other"`
	Recent  rateJSON     `json:"recent"`
}

type destCountryJSON struct {
	CC    string `json:"cc"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type destOrgJSON struct {
	ASN   string `json:"asn"`
	Org   string `json:"org"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type destDomainJSON struct {
	Name  string   `json:"name"`
	App   *appJSON `json:"app"`
	Down  uint64   `json:"down"`
	Up    uint64   `json:"up"`
	Other bool     `json:"other"`
}

type destinationsJSON struct {
	Countries []destCountryJSON `json:"countries"`
	Orgs      []destOrgJSON     `json:"orgs"`
	Domains   []destDomainJSON  `json:"domains"`
}

type flowRow struct {
	End     string  `json:"end"`
	Start   string  `json:"start"`
	MAC     string  `json:"mac"`
	Client  string  `json:"client"`
	Dir     string  `json:"dir"`
	Local   string  `json:"local"`
	Remote  string  `json:"remote"`
	Proto   uint8   `json:"proto"`
	Name    string  `json:"name"`
	NameSrc string  `json:"nameSrc"`
	App     appJSON `json:"app"`
	CC      string  `json:"cc"`
	ASN     uint32  `json:"asn"`
	Org     string  `json:"org"`
	Up      uint64  `json:"up"`
	Down    uint64  `json:"down"`
	Evicted bool    `json:"evicted"`
	// WinUp and WinDown are what a live flow moved over the window asked for.
	WinUp   uint64 `json:"winUp"`
	WinDown uint64 `json:"winDown"`
}

type flowsPage struct {
	Rows []flowRow `json:"rows"`
	Next string    `json:"next"`
}

// liveJSON is what a device is doing now. Every rate is bytes per second over
// the flow table's last Sync interval; StepSec is that interval, which is
// 2 s while somebody watches and longer otherwise.
type liveJSON struct {
	At      string          `json:"at"`
	StepSec float64         `json:"stepSec"`
	Rate    rateJSON        `json:"rate"`
	Flows   int             `json:"flows"`
	Apps    []liveAppJSON   `json:"apps"`
	Conns   []liveConnJSON  `json:"conns"`
	Places  []livePlaceJSON `json:"places"`
	Series  []livePointJSON `json:"series"`
}

type liveAppJSON struct {
	App   appJSON `json:"app"`
	Down  float64 `json:"down"`
	Up    float64 `json:"up"`
	Flows int     `json:"flows"`
	// Domain, Src, CC and Org are the app's busiest connection now.
	Domain string `json:"domain"`
	Src    string `json:"src"`
	CC     string `json:"cc"`
	Org    string `json:"org"`
	// Spark is the app's down rate at each of the ring's newest points,
	// oldest first.
	Spark []float64 `json:"spark"`
}

type liveConnJSON struct {
	App   appJSON `json:"app"`
	Name  string  `json:"name"`
	Src   string  `json:"src"`
	CC    string  `json:"cc"`
	Org   string  `json:"org"`
	ASN   uint32  `json:"asn"`
	Proto uint8   `json:"proto"`
	Port  uint16  `json:"port"`
	// Local and Remote are the connection's two ends as address:port, the
	// device's first: what the cut route takes to name this connection.
	Local     string  `json:"local"`
	Remote    string  `json:"remote"`
	Down      float64 `json:"down"`
	Up        float64 `json:"up"`
	BytesDown uint64  `json:"bytesDown"`
	BytesUp   uint64  `json:"bytesUp"`
	DurSec    float64 `json:"durSec"`
}

type livePlaceJSON struct {
	CC    string  `json:"cc"`
	Org   string  `json:"org"`
	Down  float64 `json:"down"`
	Up    float64 `json:"up"`
	Other bool    `json:"other"`
}

// livePointJSON is one Sync of the ring: bytes per second per app id over
// the StepSec before T.
type livePointJSON struct {
	T       string             `json:"t"`
	StepSec float64            `json:"stepSec"`
	Down    map[string]float64 `json:"down"`
	Up      map[string]float64 `json:"up"`
}
