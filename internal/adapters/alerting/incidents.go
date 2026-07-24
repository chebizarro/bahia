// Package alerting adapts Alertmanager-style alerts to the fleet incidents hub.
package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

const (
	IncidentsGroupID = "incidents"
	NIP29ChatKind    = 9
)

type Alert struct {
	Name        string
	Severity    string
	Summary     string
	Description string
	RunbookURL  string
	StartsAt    time.Time
	Labels      map[string]string
}

type IncidentPayload struct {
	Schema      string            `json:"schema"`
	GroupID     string            `json:"group_id"`
	Alert       string            `json:"alert"`
	Severity    string            `json:"severity"`
	Summary     string            `json:"summary"`
	Description string            `json:"description,omitempty"`
	RunbookURL  string            `json:"runbook_url"`
	StartsAt    string            `json:"starts_at"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type Publisher interface {
	PublishIncident(context.Context, int, string, []byte) error
}

type Dispatcher struct {
	publisher Publisher
	dryRun    bool
}

func NewDispatcher(publisher Publisher, dryRun bool) *Dispatcher {
	return &Dispatcher{publisher: publisher, dryRun: dryRun}
}

func (d *Dispatcher) Dispatch(ctx context.Context, alert Alert) ([]byte, error) {
	body, err := RenderIncident(alert)
	if err != nil {
		return nil, err
	}
	if d.dryRun {
		return body, nil
	}
	if d.publisher == nil {
		return nil, errors.New("incident publisher is not configured")
	}
	if err := d.publisher.PublishIncident(ctx, NIP29ChatKind, IncidentsGroupID, body); err != nil {
		return nil, err
	}
	return body, nil
}

func RenderIncident(alert Alert) ([]byte, error) {
	labels := make(map[string]string, len(alert.Labels))
	keys := make([]string, 0, len(alert.Labels))
	for key := range alert.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		labels[key] = alert.Labels[key]
	}
	return json.Marshal(IncidentPayload{
		Schema: "bahia.incident.v1", GroupID: IncidentsGroupID,
		Alert: alert.Name, Severity: alert.Severity, Summary: alert.Summary,
		Description: alert.Description, RunbookURL: alert.RunbookURL,
		StartsAt: alert.StartsAt.UTC().Format(time.RFC3339), Labels: labels,
	})
}
