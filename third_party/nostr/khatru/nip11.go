package khatru

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
)

func (rl *Relay) HandleNIP11(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/nostr+json")

	info := *rl.Info
	// The copy shares SupportedNIPs' backing array, which can have spare
	// capacity (UseEventstore appends NIP-40): appending below would write
	// rl.Info's array from concurrent requests. Give each request its own
	// slice (Bahia patch, see BAHIA_PATCHES.md).
	info.SupportedNIPs = slices.Clone(info.SupportedNIPs)

	if nil != rl.DeleteEvent {
		info.AddSupportedNIP("9")
	}
	if nil != rl.Count {
		info.AddSupportedNIP("45")
	}
	if rl.Negentropy {
		info.AddSupportedNIP("77")
	}

	// resolve relative icon and banner URLs against the base URL, leaving
	// absolute URIs (http(s), data:, etc.) untouched
	baseURL := rl.getBaseURL(r)
	info.Icon = resolveRelativeURL(info.Icon, baseURL)
	info.Banner = resolveRelativeURL(info.Banner, baseURL)

	if nil != rl.OverwriteRelayInformation {
		info = rl.OverwriteRelayInformation(r.Context(), r, info)
	}

	json.NewEncoder(w).Encode(info)
}

// resolveRelativeURL joins a scheme-less (relative) reference onto baseURL using
// net/url. Absolute URIs (http(s), data:, etc.) returned unchanged.
func resolveRelativeURL(ref, baseURL string) string {
	if ref == "" {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if u.IsAbs() {
		return ref
	}
	b, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	return b.ResolveReference(u).String()
}
