package domain

// KeyEnvelopeRecord is a key-envelope record from history, used during OCK
// recovery. Defined in domain so both controlplane and adapters/nostr can
// reference it without creating an import cycle.
type KeyEnvelopeRecord struct {
	DTag    string
	Content string
}
