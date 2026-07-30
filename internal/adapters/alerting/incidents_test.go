package alerting

import (
	"context"
	"strings"
	"testing"
	"time"
)

type publisherSpy struct {
	calls   int
	kind    int
	groupID string
}

func (p *publisherSpy) PublishIncident(_ context.Context, kind int, groupID string, _ []byte) error {
	p.calls++
	p.kind = kind
	p.groupID = groupID
	return nil
}

func TestDispatcherDryRunRendersWithoutPublishing(t *testing.T) {
	spy := &publisherSpy{}
	dispatcher := NewDispatcher(spy, true)
	body, err := dispatcher.Dispatch(t.Context(), Alert{
		Name: "BahiaDriftStuck", Severity: "critical",
		Summary: "drift stuck", RunbookURL: "/docs/runbooks/ws6-alerts.md#bahiadriftstuck",
		StartsAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		Labels:   map[string]string{"service": "api", "environment": "production"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 0 {
		t.Fatalf("dry run published %d events", spy.calls)
	}
	for _, want := range []string{
		`"schema":"bahia.incident.v1"`, `"group_id":"incidents"`,
		`"alert":"BahiaDriftStuck"`, `"severity":"critical"`,
		`"runbook_url":"/docs/runbooks/ws6-alerts.md#bahiadriftstuck"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("payload missing %s: %s", want, body)
		}
	}
}

func TestDispatcherLiveUsesNIP29IncidentsBoundary(t *testing.T) {
	spy := &publisherSpy{}
	dispatcher := NewDispatcher(spy, false)
	if _, err := dispatcher.Dispatch(t.Context(), Alert{
		Name: "BahiaRelayDegraded", Severity: "warning", StartsAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Fatalf("live dispatch calls = %d, want 1", spy.calls)
	}
	if spy.kind != NIP29ChatKind || spy.groupID != IncidentsGroupID {
		t.Fatalf("published kind/group = %d/%q", spy.kind, spy.groupID)
	}
}
