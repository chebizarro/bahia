package test

import (
	"os"
	"testing"

	"fiatjaf.com/nostr/sdk/hints/lmdbh"
)

func TestLMDBHints(t *testing.T) {
	path := "/tmp/tmpsdkhintslmdb"
	os.RemoveAll(path)

	hdb, err := lmdbh.NewLMDBHints(path)
	if err != nil {
		t.Fatal(err)
	}
	defer hdb.Close()

	runTestWith(t, hdb)
}
