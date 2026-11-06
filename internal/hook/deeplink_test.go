package hook

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestDeepLink(t *testing.T) {
	const teams = "https://teams.microsoft.com/v2/"
	cases := []struct {
		name string
		ev   Event
		want string
	}{
		{
			"chat id in tag",
			Event{Tag: "19:abc@thread.v2"},
			"https://teams.microsoft.com/l/chat/19%3Aabc%40thread.v2/conversations?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"channel id + message id in data",
			Event{Tag: "", Data: json.RawMessage(`{"threadId":"19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2","messageId":"1648741500652"}`)},
			"https://teams.microsoft.com/l/message/19%3A3997a8734ee5432bb9cdedb7c432ae7d%40thread.tacv2/1648741500652",
		},
		{
			"unq.gbl.spaces chat with message id",
			Event{Tag: "19:253f5895_f1b94dcf@unq.gbl.spaces", Data: json.RawMessage(`{"messageId":"1563480968434"}`)},
			"https://teams.microsoft.com/l/message/19%3A253f5895_f1b94dcf%40unq.gbl.spaces/1563480968434?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"group chat (thread.v2) with message id gets the chat context",
			Event{Tag: "19:abc@thread.v2", Data: json.RawMessage(`{"chatId":"19:abc@thread.v2","messageId":"1648741500652"}`)},
			"https://teams.microsoft.com/l/message/19%3Aabc%40thread.v2/1648741500652?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"channel id without a message id → teamsURL (no documented link without groupId)",
			Event{Tag: "19:carol@thread.tacv2"},
			teams,
		},
		{
			"messageId key wins over an earlier 13-digit timestamp",
			Event{Data: json.RawMessage(`{"a_ts":1700000000000,"channelId":"19:c@thread.tacv2","messageId":"1648741500888"}`)},
			"https://teams.microsoft.com/l/message/19%3Ac%40thread.tacv2/1648741500888",
		},
		{
			"numeric and nested messageId, any case",
			Event{Data: json.RawMessage(`{"meta":{"MessageID":1648741500999},"thread":"19:c@thread.tacv2"}`)},
			"https://teams.microsoft.com/l/message/19%3Ac%40thread.tacv2/1648741500999",
		},
		{
			"19-digit clientmessageid is not a message id",
			Event{Data: json.RawMessage(`{"chatId":"19:x@thread.v2","clientmessageid":"8301234567890123456"}`)},
			"https://teams.microsoft.com/l/chat/19%3Ax%40thread.v2/conversations?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"messageId key with a non-id value falls back to the scan",
			Event{Data: json.RawMessage(`{"chatId":"19:x@thread.v2","messageId":"abc","other":"1648741500111"}`)},
			"https://teams.microsoft.com/l/message/19%3Ax%40thread.v2/1648741500111?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"48:notes",
			Event{Tag: "48:notes"},
			"https://teams.microsoft.com/l/chat/48%3Anotes/conversations?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
		{
			"no usable id → teamsURL",
			Event{Tag: "not-an-id", Body: "hello"},
			teams,
		},
		{
			"message id without a thread id → teamsURL",
			Event{Data: json.RawMessage(`{"messageId":"1648741500652"}`)},
			teams,
		},
		{
			"13-digit number inside the thread id is not taken as the message id",
			Event{Tag: "19:1234567890123@thread.v2"},
			"https://teams.microsoft.com/l/chat/19%3A1234567890123%40thread.v2/conversations?context=%7B%22contextType%22%3A%22chat%22%7D",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeepLink(teams, tc.ev)
			if got != tc.want {
				t.Fatalf("DeepLink = %q, want %q", got, tc.want)
			}
			u, err := url.Parse(got)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatalf("result is not an http(s) URL: %q", got)
			}
		})
	}
}

func TestDeepLinkStaysOnOrigin(t *testing.T) {
	// A hostile id that tries to break out of the path must not change the origin.
	const teams = "http://127.0.0.1:8080/v2/"
	ev := Event{Tag: "19:abc@thread.v2", Data: json.RawMessage(`{"x":"../../@evil.com/"}`)}
	got := DeepLink(teams, ev)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:8080" {
		t.Fatalf("deep link left the origin: %q (host %q)", got, u.Host)
	}
	if !strings.HasPrefix(got, "http://127.0.0.1:8080/l/") {
		t.Fatalf("unexpected path: %q", got)
	}
}

func TestDeepLinkEscapesID(t *testing.T) {
	// '%' in an id must be escaped, so a crafted id cannot smuggle path segments.
	got := DeepLink("https://teams.microsoft.com/v2/", Event{Tag: "19:a%2F..%2F..%2Fx@thread.v2"})
	want := "https://teams.microsoft.com/l/chat/19%3Aa%252F..%252F..%252Fx%40thread.v2/conversations?context=%7B%22contextType%22%3A%22chat%22%7D"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDeepLinkBadBase(t *testing.T) {
	for _, base := range []string{"://nope", "ftp://teams", "not a url", ""} {
		if got := DeepLink(base, Event{Tag: "19:abc@thread.v2"}); got != base {
			t.Errorf("DeepLink(%q) = %q, want the base unchanged", base, got)
		}
	}
}
