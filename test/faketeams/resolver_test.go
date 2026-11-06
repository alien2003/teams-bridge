package faketeams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/hook"
)

// Resolver scenarios on chat lists shaped like the measured Teams v2 markup, evaluated by the real
// preload in headless Edge (synthetic ids only). They cover what an independent review reproduced
// against the first resolver: group vs the sender's 1:1 chat, posts that belong to no listed chat,
// the open conversation's pane, short and repeated texts, truncation, list order.

const (
	rTenant = "00000000-1111-4222-8333-000000000000"
	rPX     = "19:uni01_projectx@thread.v2"
	rA11    = "19:00000000-0000-4000-8000-0000000000a1_00000000-0000-4000-8000-0000000000ee@unq.gbl.spaces"
	rDesign = "19:uni01_design@thread.v2"
	rUnnam  = "19:uni01_unnamed@thread.v2"
	rMeet   = "19:meeting_ZjA2NTk1MDctN2VkNC00ODg1LWIzNjQtYWNkYmUyMTI1ZGNi@thread.v2"
)

func rMRI(n string) string { return "8:orgid:00000000-0000-4000-8000-0000000000" + n }

var rAlice, rBob, rCarol, rDave = rMRI("a1"), rMRI("b0"), rMRI("c0"), rMRI("d0")

func rIcon(m string) string {
	return "https://teams.cloud.microsoft/api/mt/part/zz-01/beta/users/" + rTenant + "/profilepicturev2/" + m + "?displayname=X"
}

func rAvatar(m string) string {
	return `<div class="avatar"><div role="button" data-person-mri="` + m + `"><img alt="" src="/api/mt/part/zz-01/beta/users/` + rTenant + `/profilepicturev2/` + m + `?displayname=x"/></div></div>`
}

func rItem(conv, title, preview, presence string, avatars ...string) string {
	lb := "title-chat-list-item_" + conv
	if presence != "" {
		lb += " cn-presence:" + presence
	}
	var av strings.Builder
	for _, a := range avatars {
		av.WriteString(rAvatar(a))
	}
	return `<div role="treeitem" data-fui-tree-item-value="OneGQL_ChatListItem_` + conv + `" id="chat-list-item_` + conv + `" aria-labelledby="` + lb + `">` +
		`<div class="row"><div class="avatar-stack">` + av.String() + `</div>` +
		`<span id="title-chat-list-item_` + conv + `">` + title + `</span><span class="time">10:42</span><span class="preview">` + preview + `</span></div></div>`
}

func rList(items ...string) string {
	return `<div role="tree" aria-label="Chats">` + strings.Join(items, "\n") + `</div>`
}

// rPane is the open conversation; onAncestor puts its id on an ancestor of the sender's messages.
func rPane(conv string, onAncestor bool, msgs int, sender string) string {
	var b strings.Builder
	if onAncestor {
		b.WriteString(`<section data-track-thread-id="` + conv + `"><div role="list">`)
	} else {
		b.WriteString(`<section><button data-track-thread-id="` + conv + `">x</button><div role="list">`)
	}
	for i := 0; i < msgs; i++ {
		b.WriteString(`<div class="msg">` + rAvatar(sender) + `<div>message ` + fmt.Sprint(i) + `</div></div>`)
	}
	b.WriteString(`</div></section>`)
	return b.String()
}

type resolverPage struct {
	srv   *httptest.Server
	sess  *browser.Session
	mu    sync.Mutex
	pages map[string]string
	got   []string
}

func startResolverPage(t *testing.T) *resolverPage {
	t.Helper()
	path := devBrowser(t)
	rp := &resolverPage{pages: map[string]string{"/": "<!doctype html><html><body>start</body></html>"}}
	rp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rp.mu.Lock()
		h, ok := rp.pages[r.URL.Path]
		rp.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, h)
	}))
	t.Cleanup(rp.srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	proc, err := browser.Launch(ctx, browser.LaunchOptions{Path: path, Home: t.TempDir(), Headless: true, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		proc.Close(c)
	})
	rp.sess, err = browser.Attach(ctx, proc.WSURL, browser.SessionOptions{TeamsURL: rp.srv.URL + "/", ReportHidden: true, Logger: quiet,
		PermissionOrigins: []string{rp.srv.URL}}, browser.Handlers{OnBinding: func(_, p string) {
		rp.mu.Lock()
		rp.got = append(rp.got, p)
		rp.mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.sess.Close)
	// Attach starts the first navigation without waiting for it; let it finish before ours.
	for deadline := time.Now().Add(20 * time.Second); ; {
		var ready bool
		c, cc := context.WithTimeout(ctx, 2*time.Second)
		err := rp.sess.Evaluate(c, `document.readyState === "complete"`, &ready)
		cc()
		if err == nil && ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first page did not load: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return rp
}

func (rp *resolverPage) show(t *testing.T, name, body string) {
	t.Helper()
	rp.mu.Lock()
	rp.pages["/"+name] = "<!doctype html><html><head><meta charset=utf-8><title>t</title></head><body><main>" + body + "</main></body></html>"
	rp.got = nil
	rp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rp.sess.Navigate(ctx, rp.srv.URL+"/"+name); err != nil {
		t.Fatal(err)
	}
}

// resolve calls the preload's own conversationOf on the current page (exposed by instrumenting a
// copy of the script; the production preload exposes nothing to the page).
func (rp *resolverPage) resolve(t *testing.T, sender, title, body string) string {
	t.Helper()
	inst := strings.Replace(hook.PreloadJS, `"use strict";`, `"use strict"; window.__T = { conversationOf: conversationOf };`, 1)
	if inst == hook.PreloadJS {
		t.Fatal("instrumentation failed")
	}
	js, _ := json.Marshal([]string{rIcon(sender), title, body})
	var out struct {
		ID string `json:"id"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	expr := "delete window[Symbol.for('tbd.hook.installed')];" + inst + `;(function(a){return window.__T.conversationOf(a[0],a[1],a[2]);})(` + string(js) + `)`
	if err := rp.sess.Evaluate(ctx, expr, &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func TestResolverScenarios(t *testing.T) {
	rp := startResolverPage(t)
	long := "this is a fairly long message that Teams cuts in the chat list preview"
	cases := []struct {
		name, html, sender, title, body, want string
	}{
		{"group message, the group's avatar shows the sender, sender also has a 1:1",
			rList(rItem(rPX, "Project X", "Alice: see the doc", "", rAlice, rBob), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "see the doc", rPX},
		{"group message, the group's avatar shows OTHER members",
			rList(rItem(rPX, "Project X", "Alice: see the doc", "", rBob, rCarol), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "see the doc", rPX},
		{"group message, group with a custom picture",
			rList(rItem(rPX, "Project X", "Alice: see the doc", ""), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "see the doc", rPX},
		{"short group message 'ok'",
			rList(rItem(rPX, "Project X", "Alice: ok", "", rAlice, rBob), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "ok", rPX},
		{"emoji-only group message",
			rList(rItem(rPX, "Project X", "Alice: 👍", "", rBob), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "👍", rPX},
		{"same text also the 1:1's older preview: the chat on top wins",
			rList(rItem(rPX, "Project X", "Alice: Thanks!", "", rAlice, rBob), rItem(rA11, "Alice", "Thanks!", rAlice, rAlice)), rAlice, "Alice", "Thanks!", rPX},
		{"unnamed group whose name contains the sender does not steal the message",
			rList(rItem(rPX, "Project X", "Alice: see the doc", "", rBob, rCarol), rItem(rUnnam, "Alice, Bob, Carol", "Bob: lunch?", rAlice, rAlice, rBob)), rAlice, "Alice", "see the doc", rPX},
		{"the open chat's pane (id on an ancestor) full of the sender's messages does not count",
			rList(rItem(rPX, "Project X", "Alice: see the doc", "", rBob, rCarol), rItem(rDesign, "Design", "Alice: ok", "", rBob, rDave)) + rPane(rDesign, true, 6, rAlice), rAlice, "Alice", "see the doc", rPX},
		{"1:1 message while a group full of the sender's messages is open",
			rList(rItem(rA11, "Alice", "new thing", rAlice, rAlice), rItem(rDesign, "Design", "Alice: ok", "", rBob, rDave)) + rPane(rDesign, true, 6, rAlice), rAlice, "Alice", "new thing", rA11},
		{"channel post (not in the chat list) → nothing (the phone opens Teams)",
			rList(rItem(rPX, "Project X", "Bob: x", "", rBob, rCarol), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "deploy is done", ""},
		{"1:1 chat not rendered (virtualised) → nothing, not a group with the sender",
			rList(rItem(rPX, "Project X", "Bob: x", "", rBob), rItem(rUnnam, "Alice, Bob", "Bob: lunch?", "", rAlice, rBob)), rAlice, "Alice", "are you there?", ""},
		{"no evidence at all → nothing",
			rList(rItem(rPX, "Project X", "Bob: x", "", rBob), rItem(rDesign, "Design", "Dave: y", "", rDave)), rAlice, "Alice", "zzz qqq", ""},
		{"two groups show the same text → first in the list",
			rList(rItem(rPX, "Project X", "Alice: ok thanks", ""), rItem(rDesign, "Design", "Alice: ok thanks", "")), rAlice, "Alice", "ok thanks", rPX},
		{"meeting chat",
			rList(rItem(rMeet, "Weekly sync", "Alice: see the doc", "", rBob, rCarol), rItem(rA11, "Alice", "hi", rAlice, rAlice)), rAlice, "Alice", "see the doc", rMeet},
		{"preview truncated with an ellipsis",
			rList(rItem(rA11, "Alice", long[:40]+"…", rAlice, rAlice), rItem(rPX, "Project X", "Bob: x", "", rBob)), rAlice, "Alice", long, rA11},
		{"a chat titled like the body is not a preview match",
			rList(rItem(rDesign, "see the doc", "Dave: y", ""), rItem(rPX, "Project X", "Alice: see the doc", "", rBob)), rAlice, "Alice", "see the doc", rPX},
		{"a body with line breaks matches its one-line preview",
			rList(rItem(rA11, "Alice", "first line second line", rAlice, rAlice)), rAlice, "Alice", "first line\nsecond line", rA11},
	}
	for i, tc := range cases {
		rp.show(t, fmt.Sprintf("s%d.html", i), tc.html)
		if got := rp.resolve(t, tc.sender, tc.title, tc.body); got != tc.want {
			t.Errorf("%s: resolved %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestResolverFlows drives the real notification path (window.Notification → deferred report).
func TestResolverFlows(t *testing.T) {
	rp := startResolverPage(t)
	n := func(body string) string {
		b, _ := json.Marshal(body)
		ic, _ := json.Marshal(rIcon(rAlice))
		return `new Notification("Alice",{body:` + string(b) + `,icon:` + string(ic) + `,timestamp:Date.now(),tag:"",requireInteraction:true,dir:"auto"});`
	}
	wait := func(want int) []hook.Event {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			rp.mu.Lock()
			var evs []hook.Event
			for _, p := range rp.got {
				if ev, err := hook.Parse(p); err == nil && ev.Kind == hook.KindNotification {
					evs = append(evs, ev)
				}
			}
			rp.mu.Unlock()
			if len(evs) >= want || time.Now().After(deadline) {
				return evs
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	rp.mu.Lock()
	rp.pages["/blank.html"] = "<!doctype html><html><body>blank</body></html>"
	rp.mu.Unlock()

	// A reload right after the notification must not lose it.
	rp.show(t, "reload.html", rList(rItem(rPX, "Project X", "Alice: see the doc", "", rBob))+
		"<script>"+n("see the doc")+`setTimeout(function(){location.href="/blank.html"},50);</script>`)
	if evs := wait(1); len(evs) != 1 || evs[0].Conversation != rPX {
		t.Errorf("reload after the notification: %d report(s) %+v", len(evs), evs)
	}

	// A burst in one group: each message is resolved while its preview is still shown.
	rp.show(t, "burst.html", rList(rItem(rPX, "Project X", "Alice: first", "", rBob), rItem(rA11, "Alice", "hi", rAlice, rAlice))+
		"<script>"+n("first")+`setTimeout(function(){document.querySelector(".preview").textContent="Alice: second";`+n("second")+`},300);</script>`)
	evs := wait(2)
	if len(evs) != 2 {
		t.Fatalf("burst: %d reports", len(evs))
	}
	for _, ev := range evs {
		if ev.Conversation != rPX || ev.Resolve != "items:2,matches:1" {
			t.Errorf("burst %q: conversation %q resolve %q", ev.Body, ev.Conversation, ev.Resolve)
		}
	}
}
