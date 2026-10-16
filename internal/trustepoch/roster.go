package trustepoch

// Consumer is a stable verifier roster entry. Only enforced consumers may
// acknowledge a trust transition. Pending entries always remain missing in
// the recovery engine's verify report, even if an untrusted ack appears.
type Consumer struct {
	ID          string
	Enforcement string
	Reason      string // non-empty only when enforcement remains pending
}

var roster = []Consumer{
	{ID: "daemon", Enforcement: "enforced"},
	{ID: "sidecar", Enforcement: "enforced"},
	{ID: "web", Enforcement: "enforced"},
	{ID: "cli", Enforcement: "enforced"},
	{ID: "dns", Enforcement: "enforced"},
	{ID: "loom", Enforcement: "enforced"},
	{ID: "soulfactory", Enforcement: "pending", Reason: "cli_nostr_client_has_no_live_operator_epoch_subscription_or_inventory_seam"},
	{ID: "hiveci", Enforcement: "enforced"},
	{ID: "pinned-runtimes", Enforcement: "pending", Reason: "edge_image_admission_has_no_signed_epoch_source_or_durable_high_water_mark"},
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
