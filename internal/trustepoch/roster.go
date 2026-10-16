package trustepoch

// Consumer is a stable verifier roster entry. Only enforced consumers may
// acknowledge a trust transition. Pending entries always remain missing in
// the recovery engine's verify report, even if an untrusted ack appears.
type Consumer struct {
	ID          string
	Enforcement string
}

var roster = []Consumer{
	{ID: "daemon", Enforcement: "enforced"},
	{ID: "sidecar", Enforcement: "enforced"},
	{ID: "web", Enforcement: "enforced"},
	{ID: "cli", Enforcement: "enforced"},
	{ID: "dns", Enforcement: "pending"},
	{ID: "loom", Enforcement: "pending"},
	{ID: "soulfactory", Enforcement: "pending"},
	{ID: "hiveci", Enforcement: "pending"},
	{ID: "pinned-runtimes", Enforcement: "pending"},
}

func Consumers() []Consumer { return append([]Consumer(nil), roster...) }

func ConsumerIDs() []string {
	ids := make([]string, 0, len(roster))
	for _, consumer := range roster {
		ids = append(ids, consumer.ID)
	}
	return ids
}

func Registered(id string) bool {
	for _, consumer := range roster {
		if consumer.ID == id {
			return true
		}
	}
	return false
}

// MissingAcknowledgments returns every pending consumer plus enforced
// consumers without a verified acknowledgment for the requested epoch.
func MissingAcknowledgments(acknowledged map[string]bool) []string {
	missing := make([]string, 0, len(roster))
	for _, consumer := range roster {
		if consumer.Enforcement == "pending" || !acknowledged[consumer.ID] {
			missing = append(missing, consumer.ID)
		}
	}
	return missing
}
