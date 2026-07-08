package bbolth

import (
	"fmt"
	"math"
	"slices"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/sdk/hints"
	"go.etcd.io/bbolt"
)

var _ hints.HintsDB = (*BoltHints)(nil)

var (
	hintsBucket = []byte("hints")
)

type BoltHints struct {
	db *bbolt.DB
}

func NewBoltHints(path string) (*BoltHints, error) {
	db, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open bbolt db: %w", err)
	}

	// Create the hints bucket
	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(hintsBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	return &BoltHints{db: db}, nil
}

func (bh *BoltHints) Close() {
	bh.db.Close()
}

func (bh *BoltHints) Save(pubkey nostr.PubKey, relay string, hintkey hints.HintKey, ts nostr.Timestamp) {
	if now := nostr.Now(); ts > now {
		ts = now
	}

	err := bh.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(hintsBucket)
		k := encodeKey(pubkey, relay)
		var tss timestamps

		if v := b.Get(k); v != nil {
			tss = parseValue(v)
		}

		if tss[hintkey] < ts {
			tss[hintkey] = ts
			return b.Put(k, encodeValue(tss))
		}

		return nil
	})
	if err != nil {
		nostr.InfoLogger.Printf("[sdk/hints/bbolt] unexpected error on save: %s\n", err)
	}
}

func (bh *BoltHints) TopN(pubkey nostr.PubKey, n int) []string {
	type relayScore struct {
		relay string
		score int64
	}

	scores := make([]relayScore, 0, n)
	err := bh.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(hintsBucket)
		c := b.Cursor()

		prefix := pubkey[:]
		for k, v := c.Seek(prefix); k != nil && len(k) >= 32 && string(k[:32]) == string(prefix); k, v = c.Next() {
			relay := string(k[32:])
			tss := parseValue(v)
			scores = append(scores, relayScore{relay, tss.sum()})
		}
		return nil
	})
	if err != nil {
		nostr.InfoLogger.Printf("[sdk/hints/bbolt] unexpected error on topn: %s\n", err)
		return nil
	}

	slices.SortFunc(scores, func(a, b relayScore) int {
		return int(b.score - a.score)
	})

	result := make([]string, 0, n)
	for i, rs := range scores {
		if i >= n {
			break
		}
		result = append(result, rs.relay)
	}
	return result
}

func (bh *BoltHints) GetDetailedScores(pubkey nostr.PubKey, n int) []hints.RelayScores {
	type relayScore struct {
		relay string
		tss   timestamps
		score int64
	}

	scores := make([]relayScore, 0, n)
	err := bh.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(hintsBucket)
		c := b.Cursor()

		prefix := pubkey[:]
		for k, v := c.Seek(prefix); k != nil && len(k) >= 32 && string(k[:32]) == string(prefix); k, v = c.Next() {
			relay := string(k[32:])
			tss := parseValue(v)
			scores = append(scores, relayScore{relay, tss, tss.sum()})
		}
		return nil
	})
	if err != nil {
		return nil
	}

	slices.SortFunc(scores, func(a, b relayScore) int {
		return int(b.score - a.score)
	})

	result := make([]hints.RelayScores, 0, n)
	for i, rs := range scores {
		if i >= n {
			break
		}
		result = append(result, hints.RelayScores{
			Relay:  rs.relay,
			Scores: rs.tss,
			Sum:    rs.score,
		})
	}
	return result
}

func (bh *BoltHints) PrintScores() {
	fmt.Println("= print scores")

	err := bh.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(hintsBucket)
		c := b.Cursor()

		var lastPubkey nostr.PubKey
		i := 0

		for k, v := c.First(); k != nil; k, v = c.Next() {
			pubkey, relay := parseKey(k)
			tss := parseValue(v)

			if pubkey != lastPubkey {
				fmt.Println("== relay scores for", pubkey)
				lastPubkey = pubkey
				i = 0
			} else {
				i++
			}

			fmt.Printf("  %3d :: %30s ::> %12d\n", i, relay, tss.sum())
		}
		return nil
	})
	if err != nil {
		nostr.InfoLogger.Printf("[sdk/hints/bbolt] unexpected error on print: %s\n", err)
	}
}

type timestamps [4]nostr.Timestamp

func (tss timestamps) sum() int64 {
	now := nostr.Now() + 24*60*60
	var sum int64
	for i, ts := range tss {
		if ts == 0 {
			continue
		}
		value := float64(hints.HintKey(i).BasePoints()) * 10000000000 / math.Pow(float64(max(now-ts, 1)), 1.3)
		sum += int64(value)
	}
	return sum
}
