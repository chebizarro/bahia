package sdk

import (
	"context"
	"sync"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/sdk/cache"
	cache_memory "fiatjaf.com/nostr/sdk/cache/memory"
)

// ListItemOrSet is a single item in a list that can contain sets.
//
// If Pointer is nil then this is a plain item and Item is set.
// Otherwise Pointer refers to an addressable event that represents a set,
// which is then fetched and parsed into Set.
//
// This only works when the underlying item's V is string, since the string
// value is used both for items and for set references (the pointer's tag
// reference), which is necessary for correct deduplication.
type ListItemOrSet[I TagItemWithValue[string]] struct {
	Item I // nil if this entry is a set reference

	// nil if this entry is an item
	Pointer nostr.Pointer
	Set     GenericList[string, I]
}

func (los ListItemOrSet[I]) Value() string {
	if los.Pointer == nil {
		return los.Item.Value()
	}

	return los.Pointer.AsTagReference()
}

func fetchListWithSets[I TagItemWithValue[string]](
	sys *System,
	ctx context.Context,
	pubkey nostr.PubKey,
	actualKind nostr.Kind,
	replaceableIndex replaceableIndex,
	parseTag func(nostr.Tag) (ListItemOrSet[I], bool),
	parseSetItemTag func(nostr.Tag) (I, bool),
	cache cache.Cache32[GenericList[string, ListItemOrSet[I]]],
) (fl GenericList[string, ListItemOrSet[I]]) {
	fl, _ = fetchGenericList(sys, ctx, pubkey, actualKind, replaceableIndex, parseTag, cache)

	wg := sync.WaitGroup{}
	for i, los := range fl.Items {
		if los.Pointer == nil {
			continue
		}

		wg.Add(1)
		go func(i int, los ListItemOrSet[I]) {
			defer wg.Done()

			evt, _, _ := sys.FetchSpecificEvent(ctx, los.Pointer, FetchSpecificEventParameters{
				SaveToLocalStore: true,
			})
			if evt == nil {
				return
			}

			fl.Items[i].Set = GenericList[string, I]{
				Event: evt,
				Items: parseItemsFromEventTags(*evt, parseSetItemTag),
			}
		}(i, los)
	}
	wg.Wait()

	return fl
}

// -- relay sets

func (sys *System) FetchFavoriteRelaysWithSets(ctx context.Context, pubkey nostr.PubKey) GenericList[string, ListItemOrSet[RelayURL]] {
	sys.favoriteRelaysWithSetsCacheOnce.Do(func() {
		if sys.FavoriteRelaysWithSetsListCache == nil {
			sys.FavoriteRelaysWithSetsListCache = cache_memory.New[GenericList[string, ListItemOrSet[RelayURL]]](1000)
		}
	})

	return fetchListWithSets(sys, ctx, pubkey, 10012, kind_10012, parseRelayOrSetRef, parseRelayURL, sys.FavoriteRelaysWithSetsListCache)
}

func parseRelayOrSetRef(tag nostr.Tag) (ls ListItemOrSet[RelayURL], ok bool) {
	switch tag[0] {
	case "relay":
		rl, ok := parseRelayURL(tag)
		if !ok {
			return ls, false
		}
		ls.Item = rl
		return ls, true
	case "a":
		pointer, err := nostr.EntityPointerFromTag(tag)
		if err != nil || pointer.Kind != 30002 {
			return ls, false
		}
		ls.Pointer = pointer
		return ls, true
	default:
		return ls, false
	}
}

// -- emoji sets

func (sys *System) FetchEmojisWithSets(ctx context.Context, pubkey nostr.PubKey) GenericList[string, ListItemOrSet[Emoji]] {
	sys.emojisWithSetsListCacheOnce.Do(func() {
		if sys.EmojisWithSetsListCache == nil {
			sys.EmojisWithSetsListCache = cache_memory.New[GenericList[string, ListItemOrSet[Emoji]]](1000)
		}
	})

	return fetchListWithSets(sys, ctx, pubkey, 10030, kind_10030, parseEmojiOrSetRef, parseEmojiTag, sys.EmojisWithSetsListCache)
}

func parseEmojiOrSetRef(tag nostr.Tag) (ls ListItemOrSet[Emoji], ok bool) {
	switch tag[0] {
	case "emoji":
		em, ok := parseEmojiTag(tag)
		if !ok {
			return ls, false
		}
		ls.Item = em
		return ls, true
	case "a":
		pointer, err := nostr.EntityPointerFromTag(tag)
		if err != nil || pointer.Kind != 30030 {
			return ls, false
		}
		ls.Pointer = pointer
		return ls, true
	default:
		return ls, false
	}
}
