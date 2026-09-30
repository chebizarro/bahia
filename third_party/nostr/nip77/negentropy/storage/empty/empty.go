package empty

import (
	"iter"

	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage"
)

var acc storage.Accumulator

type Empty struct{}

func (Empty) Size() int { return 0 }

func (Empty) Range(begin, end int) iter.Seq2[int, negentropy.Item] {
	return func(yield func(int, negentropy.Item) bool) {}
}

func (Empty) FindLowerBound(begin, end int, value negentropy.Bound) int { return begin }

func (Empty) GetBound(idx int) negentropy.Bound {
	return negentropy.InfiniteBound
}

func (Empty) Fingerprint(begin, end int) [negentropy.FingerprintSize]byte {
	return acc.GetFingerprint(end - begin)
}
