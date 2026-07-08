package main

import (
	"fmt"
	"net/http"
	"os"

	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/khatru/grasp"
)

func main() {
	relay := khatru.NewRelay()

	db := &lmdb.LMDBBackend{Path: "/tmp/khatru-grasp-lmdb-tmp"}
	os.MkdirAll(db.Path, 0o755)
	if err := db.Init(); err != nil {
		panic(err)
	}

	relay.UseEventstore(db, 400)

	// create repository directory
	repoDir := "/tmp/khatru-grasp-repos"
	os.MkdirAll(repoDir, 0o755)

	// set up grasp server
	grasp.New(relay, repoDir)

	fmt.Println("running grasp example on :3334")
	http.ListenAndServe(":3334", relay)
}
