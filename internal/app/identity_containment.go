package app

import (
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// identityContainment keeps evidence-retention gates aligned with the one
// process-wide outbound fence. Both local stores are shared across reload
// candidates, so a new App sees the same hold immediately.
type identityContainment struct {
	admission *nostrout.Admission
	events    *localstore.Store
	outbox    *localstore.Outbox
}

func newIdentityContainment(admission *nostrout.Admission, events *localstore.Store, outbox *localstore.Outbox) *identityContainment {
	c := &identityContainment{admission: admission, events: events, outbox: outbox}
	active, _ := admission.RuntimeKillSwitch()
	c.setEvidenceHold(active)
	return c
}

func (c *identityContainment) setEvidenceHold(active bool) {
	c.events.SetEvidenceHold(active)
	c.outbox.SetEvidenceHold(active)
}

func (c *identityContainment) ActivateRuntimeKillSwitch(id string) error {
	c.setEvidenceHold(true)
	return c.admission.ActivateRuntimeKillSwitch(id)
}

func (c *identityContainment) ReleaseRuntimeKillSwitch() error {
	if err := c.admission.ReleaseRuntimeKillSwitch(); err != nil {
		return err
	}
	c.setEvidenceHold(false)
	return nil
}

func (c *identityContainment) RuntimeKillSwitch() (bool, string) {
	return c.admission.RuntimeKillSwitch()
}
func (c *identityContainment) Active() bool { active, _ := c.RuntimeKillSwitch(); return active }

func identityContainmentHealth(c *identityContainment, watcher *controlplane.IdentityIncidents) HealthCheck {
	active, id := c.RuntimeKillSwitch()
	if active {
		check := HealthCheck{Name: "identity_containment", Status: HealthStatusFail, Message: "service identity is contained; signing and mutation are disabled"}
		if id != "" {
			check.Details = map[string]string{"incident_id": id}
		}
		return check
	}
	if watcher != nil && !watcher.CaughtUp() {
		return HealthCheck{Name: "identity_containment", Status: HealthStatusWarn, Message: "identity incident subscription has not caught up"}
	}
	return HealthCheck{Name: "identity_containment", Status: HealthStatusPass, Message: "no active identity incident"}
}
