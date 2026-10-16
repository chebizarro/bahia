package identityrecovery

import (
	"context"
	"errors"
	"reflect"
	"strconv"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/docs"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// DocsRecoveryAdapter reasserts only Bahia's 30023 bahia-docs topics. It
// retains the accepted long-form body and publisher metadata exactly.
type DocsRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

func isBahiaDocs(ev nostr.Event) bool {
	if ev.Kind != nostr.Kind(kinds.LongFormContent) {
		return false
	}
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "t" && t[1] == kinds.DocsTopic {
			return true
		}
	}
	return false
}

func docsRecord(old nostr.Event) (docs.Document, int64, error) {
	if !isBahiaDocs(old) || tag(old, "d") == "" || tag(old, "title") == "" {
		return docs.Document{}, 0, errors.New("invalid Bahia docs coordinate")
	}
	var category string
	for _, t := range old.Tags {
		if len(t) >= 2 && t[0] == "t" && t[1] != kinds.DocsTopic {
			category = t[1]
			break
		}
	}
	if category == "" {
		return docs.Document{}, 0, errors.New("bahia docs category missing")
	}
	at, err := strconv.ParseInt(tag(old, "published_at"), 10, 64)
	if err != nil || at <= 0 {
		return docs.Document{}, 0, errors.New("bahia docs publication timestamp invalid")
	}
	doc := docs.Document{Topic: docs.Topic{Topic: tag(old, "d"), Title: tag(old, "title"), Category: category}, Markdown: old.Content}
	rebuilt := docs.DocsRecoveryRecord(doc, at)
	if !reflect.DeepEqual(rebuilt.Tags, old.Tags) || rebuilt.Content != old.Content {
		return docs.Document{}, 0, errors.New("typed Bahia docs builder cannot reproduce accepted head")
	}
	return doc, at, nil
}

type docsRecoveryPublisher struct {
	signer nostr.Keyer
	sink   registryRecoverySink
}

func (p docsRecoveryPublisher) PublishSignedEvent(ctx context.Context, ev *nostr.Event) error {
	if err := p.signer.SignEvent(ctx, ev); err != nil {
		return err
	}
	return p.sink.publish(ctx, *ev)
}

func (a *DocsRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{
		old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.LongFormContent)}, name: "bahia-docs",
		belongs:   isBahiaDocs,
		preflight: func(ev nostr.Event) error { _, _, err := docsRecord(ev); return err },
		publish: func(ctx context.Context, signer nostr.Keyer, old nostr.Event, sink registryRecoverySink) error {
			doc, at, err := docsRecord(old)
			if err != nil {
				return err
			}
			publisher := docs.NewNostrDocsPublisher(docs.Service{}, docsRecoveryPublisher{signer: signer, sink: sink}, nil, nil, zap.NewNop())
			return publisher.PublishRecoveredTopic(ctx, doc, at)
		},
	}
}

func (a *DocsRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}
func (a *DocsRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	return a.core().Reassert(ctx, signer)
}
func (a *DocsRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	return a.core().Verify(ctx)
}
