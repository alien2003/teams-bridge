package hook

import "testing"

func TestSenderMRI(t *testing.T) {
	for in, want := range map[string]string{
		"https://teams.live.com/api/mt/beta/users/8:live:.cid.0a1b2c3d4e5f6789/profilepicturev2?displayname=B%20B":             "8:live:.cid.0a1b2c3d4e5f6789",
		"https://teams.microsoft.com/api/mt/zz/beta/users/8%3Aorgid%3A1f2e3d4c-0000-1111-2222-333344445555/profilepicturev2": "8:orgid:1f2e3d4c-0000-1111-2222-333344445555",
		"https://example.com/avatar.png":    "",
		"https://x/users/../profilepicture": "",
		"":                                  "",
	} {
		if got := SenderMRI(in); got != want {
			t.Errorf("SenderMRI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseConversation(t *testing.T) {
	for in, want := range map[string]string{
		`"19:uni01_abc@thread.v2"`: "19:uni01_abc@thread.v2",
		`"19:a_b@unq.gbl.spaces"`:  "19:a_b@unq.gbl.spaces",
		`"48:notes"`:               "48:notes",
		`"19:x@thread.v2 extra"`:   "",
		`"javascript:alert(1)"`:    "",
		`"19:x@evil.example"`:      "",
		`7`:                        "",
	} {
		ev, err := Parse(`{"kind":"notification","title":"t","conversation":` + in + `}`)
		if in == "7" {
			if err == nil {
				t.Errorf("a number for conversation must be rejected")
			}
			continue
		}
		if err != nil || ev.Conversation != want {
			t.Errorf("conversation %s → %q, %v; want %q", in, ev.Conversation, err, want)
		}
	}
}

func TestDeepLinkUsesResolvedConversationAndPageOrigin(t *testing.T) {
	const teamsURL = "https://teams.microsoft.com/v2/"
	cases := []struct {
		name string
		ev   Event
		want string
	}{
		{"resolved chat on teams_url origin", Event{Conversation: "19:uni01_abc@thread.v2", Href: "https://teams.microsoft.com/v2/"},
			"https://teams.microsoft.com/l/chat/19:uni01_abc@thread.v2/conversations"},
		{"personal account: page on teams.live.com", Event{Conversation: "19:uni01_abc@thread.v2", Href: "https://teams.live.com/v2/?x"},
			"https://teams.live.com/l/chat/19:uni01_abc@thread.v2/conversations"},
		{"foreign page origin is ignored", Event{Conversation: "19:uni01_abc@thread.v2", Href: "https://evil.example/v2/"},
			"https://teams.microsoft.com/l/chat/19:uni01_abc@thread.v2/conversations"},
		{"resolved conversation beats the tag", Event{Conversation: "19:uni01_abc@thread.v2", Tag: "19:other@thread.v2"},
			"https://teams.microsoft.com/l/chat/19:uni01_abc@thread.v2/conversations"},
		{"nothing resolved: teams_url", Event{Href: "https://teams.live.com/v2/"}, teamsURL},
	}
	for _, tc := range cases {
		if got := DeepLink(teamsURL, tc.ev); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
