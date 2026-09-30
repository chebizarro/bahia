package test

import (
	"os"
	"testing"

	"fiatjaf.com/nostr/sdk/hints/bbolth"
)

func TestBoltHints(t *testing.T) {
	path := "/tmp/tmpsdkhintsbbolt"
	os.RemoveAll(path)

	hdb, err := bbolth.NewBoltHints(path)
	if err != nil {
		t.Fatal(err)
	}
	defer hdb.Close()

	runTestWith(t, hdb)
}
