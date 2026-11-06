package fcm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alien2003/teams-bridge/internal/proto"
)

func longMessage(body string) proto.Message {
	return proto.Message{V: proto.Version, T: proto.TypeMsg, ID: "8b0f2a8e-4a39-4b8e-9a43-0d1c2e3f4a5b", TS: 1_790_000_000,
		Title: "Alice", Body: body, URL: "https://teams.microsoft.com/v2/", Tag: "19:abc@thread"}
}

func TestSendMessage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	m := longMessage(strings.Repeat("x", 1500))
	name, sent, err := e.c.SendMessage(context.Background(), e.secret, m, nil)
	if err != nil || name == "" || sent != m {
		t.Fatalf("%q %v", name, err)
	}
	got := e.srv.Messages()
	if len(got) != 1 || got[0].SealedLen != 2076 || got[0].Decrypted == nil || *got[0].Decrypted != m {
		t.Fatalf("mock saw %+v", got)
	}
}

// FCM documents a 2048-byte data limit for topics; a rejected large payload is resent small.
func TestSendMessageFallsBackToSmallPadding(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.SetTopicDataLimit(2048)
	m := longMessage(strings.Repeat("ж", 900))
	_, sent, err := e.c.SendMessage(context.Background(), e.secret, m, nil)
	if err != nil {
		t.Fatalf("%v (%q)", err, e.srv.Rejections())
	}
	got := e.srv.Messages()
	if st := e.srv.Stats(); st.SendRequests != 2 || len(got) != 1 {
		t.Fatalf("stats %+v", st)
	}
	d := got[0].Decrypted
	if got[0].SealedLen != 1052 || d == nil || *d != sent || d.ID != m.ID || !strings.HasSuffix(d.Body, "…") ||
		!strings.HasPrefix(m.Body, strings.TrimSuffix(d.Body, "…")) {
		t.Fatalf("resent message: len %d, %+v", got[0].SealedLen, d)
	}

	// A small message is not retried on INVALID_ARGUMENT.
	e = newEnv(t, nil)
	e.srv.FailNext(http.StatusBadRequest, 1)
	if _, _, err := e.c.SendMessage(context.Background(), e.secret, longMessage("hi"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if st := e.srv.Stats(); st.SendRequests != 1 {
		t.Errorf("stats %+v", st)
	}

	// Nor is a large one on another error.
	e = newEnv(t, nil)
	e.srv.FailNext(http.StatusNotFound, 1)
	if _, _, err := e.c.SendMessage(context.Background(), e.secret, m, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if st := e.srv.Stats(); st.SendRequests != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestFitSmall(t *testing.T) {
	t.Parallel()
	jsonLen := func(m proto.Message) int {
		js, _, err := proto.EncodeJSON(m)
		if err != nil {
			t.Fatal(err)
		}
		return len(js)
	}
	short := longMessage("hello")
	if got, ok := FitSmall(short); !ok || got != short {
		t.Errorf("short message changed: %+v", got)
	}

	m := longMessage(strings.Repeat("€", 700))
	got, ok := FitSmall(m)
	if !ok || jsonLen(got)+2 > proto.SmallPad || !strings.HasSuffix(got.Body, "…") || got.Title != m.Title {
		t.Fatalf("body truncation: %v %d %+v", ok, jsonLen(got), got)
	}
	// Maximal: one more rune would not fit.
	n := utf8.RuneCountInString(got.Body) - 1
	more := got
	more.Body = string([]rune(m.Body)[:n+1]) + "…"
	if jsonLen(more)+2 <= proto.SmallPad {
		t.Errorf("truncated more than needed (%d runes kept)", n)
	}

	both := longMessage(strings.Repeat("b", 2000))
	both.Title = strings.Repeat("t", 1500)
	got, ok = FitSmall(both)
	if !ok || got.Body != "" || !strings.HasSuffix(got.Title, "…") || jsonLen(got)+2 > proto.SmallPad {
		t.Errorf("title truncation: %v %+v", ok, got)
	}

	hopeless := longMessage("x")
	hopeless.Tag = strings.Repeat("t", 1100)
	if _, ok := FitSmall(hopeless); ok {
		t.Error("a message whose tag alone exceeds 1024 bytes cannot fit")
	}
}
