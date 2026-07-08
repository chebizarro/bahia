package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/khatru/blossom"
)

func main() {
	relay := khatru.NewRelay()

	db := &lmdb.LMDBBackend{Path: "/tmp/khatru-lmdb-tmp"}
	if err := db.Init(); err != nil {
		panic(err)
	}

	relay.UseEventstore(db, 400)

	bdb := &lmdb.LMDBBackend{Path: "/tmp/khatru-lmdb-blossom-tmp"}
	if err := bdb.Init(); err != nil {
		panic(err)
	}
	bl := blossom.New(relay, "http://localhost:3334")
	bl.Store = blossom.EventStoreBlobIndexWrapper{Store: bdb, ServiceURL: bl.ServiceURL}
	bl.StoreBlob = func(ctx context.Context, sha256 string, ext string, body []byte) error {
		fmt.Println("storing", sha256, len(body))
		return nil
	}
	bl.LoadBlob = func(ctx context.Context, sha256 string, ext string) (io.ReadSeeker, *url.URL, error) {
		fmt.Println("loading", sha256)
		blob := strings.NewReader("aaaaa")
		return blob, nil, nil
	}

	fmt.Println("running on :3334")
	http.ListenAndServe(":3334", relay)
}
