package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/khatru/policies"
)

func main() {
	relay := khatru.NewRelay()

	db := &lmdb.LMDBBackend{Path: "/tmp/exclusive"}
	os.MkdirAll(db.Path, 0o755)
	if err := db.Init(); err != nil {
		panic(err)
	}

	relay.UseEventstore(db, 400)

	relay.OnEvent = policies.PreventTooManyIndexableTags(10, nil, nil)
	relay.OnRequest = policies.NoComplexFilters

	relay.OnEventSaved = func(ctx context.Context, event nostr.Event) {
	}

	fmt.Println("running on :3334")
	http.ListenAndServe(":3334", relay)
}

func deleteStuffThatCanBeFoundElsewhere() {
}
