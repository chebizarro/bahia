package sdk

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestPrepareNoteEvent(t *testing.T) {
	// prepare a dummy system, it's needed for context but won't be used in these specific tests
	sys := NewSystem()
	defer sys.Close()
	ctx := context.Background()

	tests := []struct {
		name     string
		content  string
		tags     nostr.Tags // initial tags
		wantTags nostr.Tags // expected tags after processing
		want     string     // expected content after processing
	}{
		{
			name:     "plain text",
			content:  "hello world",
			tags:     nostr.Tags{},
			wantTags: nostr.Tags{},
			want:     "hello world",
		},
		{
			name:    "with nostr: prefix, url and hashtag",
			content: "hello nostr:npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6 please visit https://banana.com/ and get your free #banana",
			tags:    nostr.Tags{},
			wantTags: nostr.Tags{
				{"p", "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"},
				{"t", "banana"},
			},
			want: "hello nostr:npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6 please visit https://banana.com/ and get your free #banana",
		},
		{
			name:    "with bare npub and bare url",
			content: "hello npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6 please visit banana.com",
			tags:    nostr.Tags{},
			wantTags: nostr.Tags{
				{"p", "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"},
			},
			want: "hello nostr:npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6 please visit https://banana.com",
		},
		{
			name:    "content with a single hashtag",
			content: "#this is a test with #nostr",
			tags:    nostr.Tags{},
			wantTags: nostr.Tags{
				{"t", "nostr"},
				{"t", "this"},
			},
			want: "#this is a test with #nostr",
		},
		{
			name:    "content with multiple hashtags",
			content: "testing #multiple #hashtags here",
			tags:    nostr.Tags{},
			wantTags: nostr.Tags{
				{"t", "multiple"},
				{"t", "hashtags"},
			},
			want: "testing #multiple #hashtags here",
		},
		{
			name:    "content with existing t tag",
			content: "when adding #tags don't add duplicate #tags to banana.social",
			tags:    nostr.Tags{{"t", "tags"}},
			wantTags: nostr.Tags{
				{"t", "tags"},
			},
			want: "when adding #tags don't add duplicate #tags to https://banana.social",
		},
		{
			name:    "content with mixed tags and hashtag",
			content: "a valid nevent1qqsr0f9w78uyy09qwmjt0kv63j4l7sxahq33725lqyyp79whlfjurwspz4mhxue69uhh56nzv34hxcfwv9ehw6nyddhqygpm7rrrljungc6q0tuh5hj7ue863q73qlheu4vywtzwhx42a7j9n5x0aedk and an invalid nevent1aaa",
			tags:    nostr.Tags{{"t", "unrelated"}},
			wantTags: nostr.Tags{
				{"t", "unrelated"},
				{"q", "37a4aef1f8423ca076e4b7d99a8cabff40ddb8231f2a9f01081f15d7fa65c1ba", "wss://zjbdksa.aswjdkn", "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"},
			},
			want: "a valid nostr:nevent1qqsr0f9w78uyy09qwmjt0kv63j4l7sxahq33725lqyyp79whlfjurwspz4mhxue69uhh56nzv34hxcfwv9ehw6nyddhqygpm7rrrljungc6q0tuh5hj7ue863q73qlheu4vywtzwhx42a7j9n5x0aedk and an invalid nevent1aaa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := &nostr.Event{
				Content: tt.content,
				Tags:    tt.tags.Clone(),
			}

			sys.PrepareNoteEvent(ctx, evt)
			require.Equal(t, tt.want, evt.Content)
			require.ElementsMatch(t, tt.wantTags, evt.Tags, "tags mismatch")
		})
	}
}
