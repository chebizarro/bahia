---
outline: deep
---

## Querying events from Google Drive

Suppose you have a bunch of events stored in text files on Google Drive and you want to serve them as a relay. You could just store each event as a separate file and use the native Google Drive search to match the queries when serving requests. It would probably not be as fast as using local database, but it would work.

```go
func main () {
	// other stuff here
	relay := khatru.NewRelay()

	relay.StoreEvent = handleEvent
	relay.QueryStored = handleQuery
	// other stuff here
}

func handleEvent(ctx context.Context, event nostr.Event) error {
	// store each event as a file on google drive
	_, err := gdriveService.Files.Create(googledrive.CreateOptions{
		Name: event.ID, // with the name set to their id
		Body: event.String(), // the body as the full event JSON
	})
	return err
}

func handleQuery(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
	// QueryEvents functions return an iterator
	return func(yield func(nostr.Event) bool) {
		if len(filter.IDs) > 0 {
			// if the query is for ids we can do a simpler name match
			for _, id := range filter.IDS {
				results, _ := gdriveService.Files.List(googledrive.ListOptions{
					Q: fmt.Sprintf("name = '%s'", id)
				})
				if len(results) > 0 {
					var evt nostr.Event
					json.Unmarshal(results[0].Body, &evt)
					if !yield(evt) {
						return
					}
				}
			}
		} else {
			// otherwise we use the google-provided search and hope it will catch tags that are in the event body
			for tagName, tagValues := range filter.Tags {
				results, _ := gdriveService.Files.List(googledrive.ListOptions{
					Q: fmt.Sprintf("fullText contains '%s'", tagValues)
				})
				for _, result := range results {
					var evt nostr.Event
					json.Unmarshal(results[0].Body, &evt)
					if filter.Match(evt) {
						if !yield(evt) {
							return
						}
					}
				}
			}
		}
	}
}
```

(Disclaimer: since I have no idea of how to properly use the Google Drive API this interface is entirely made up.)
