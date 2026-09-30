---
outline: deep
---

# Event Storage

Khatru doesn't make any assumptions about how you'll want to store events. Any function can be plugged in to the `StoreEvent`, `DeleteEvent`, `ReplaceEvent` and `QueryEvents` hooks.

However the [`eventstore`](https://fiatjaf.com/nostr/eventstore) library has adapters that you can easily plug into `khatru`'s hooks.

# Using the `eventstore` library

The library includes many different adapters -- often called "backends" --, written by different people and with different levels of quality, reliability and speed.

For all of them you start by instantiating a struct containing some basic options and a pointer (a file path for local databases, a connection string for remote databases) to the data. Then you call `.Init()` and if all is well you're ready to start storing, querying and deleting events, so you can pass the respective functions to their `khatru` counterparts. These eventstores also expose a `.Close()` function that must be called if you're going to stop using that store and keep your application open.

Here's an example with the [BoltDB](https://pkg.go.dev/fiatjaf.com/nostr/eventstore/boltdb) adapter, made for the [BoltDB](https://github.com/etcd-io/bbolt) embedded key-value database:

```go
package main

import (
	"fmt"
	"net/http"

	"fiatjaf.com/nostr/eventstore/boltdb"
	"fiatjaf.com/nostr/khatru"
)

func main() {
	relay := khatru.NewRelay()

	db := boltdb.BoltBackend{Path: "/tmp/khatru-bolt-tmp"}
	if err := db.Init(); err != nil {
		panic(err)
	}

	relay.UseEventstore(db, 500)

	fmt.Println("running on :3334")
	http.ListenAndServe(":3334", relay)
}
```

[LMDB](https://pkg.go.dev/fiatjaf.com/nostr/eventstore/lmdb) works the same way.

## Using two at a time

If you want to use two different adapters at the same time that's easy. Just use the `policies.Seq*` functions:

```go
	relay.StoreEvent = policies.SeqStore(db1.SaveEvent, db2.SaveEvent)
	relay.QueryStored = policies.SeqQuery(db1.QueryEvents, db2.QueryEvents)
```

But that will duplicate events on both and then return duplicated events on each query.

## Sharding

You can do a kind of sharding, for example, by storing some events in one store and others in another:

For example, maybe you want kind 1 events in `db1` and kind 30023 events in `db30023`:

```go
	relay.StoreEvent = func (ctx context.Context, evt nostr.Event) error {
		switch evt.Kind {
		case nostr.Kind(1):
			return db1.SaveEvent(evt)
		case nostr.Kind(30023):
			return db30023.SaveEvent(evt)
		default:
			return nil
		}
	}
	relay.QueryStored = func (ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		for _, kind := range filter.Kinds {
			switch nostr.Kind(kind) {
			case nostr.Kind(1):
				filter1 := filter
				filter1.Kinds = []nostr.Kind{1}
				return db1.QueryEvents(filter1)
			case nostr.Kind(30023):
				filter30023 := filter
				filter30023.Kinds = []nostr.Kind{30023}
				return db30023.QueryEvents(filter30023)
			default:
				return nil
			}
		}
		return nil
	}
```
