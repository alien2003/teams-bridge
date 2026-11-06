package browser

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/alien2003/teams-bridge/internal/hook"
	"github.com/alien2003/teams-bridge/test/faketeams"
)

// TestWorkerTransports: the service-worker hook reports through the worker's own __tbd binding;
// its fallback queue is drained when a report could not use the binding.
func TestWorkerTransports(t *testing.T) {
	path := requireBrowser(t)
	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proc, err := Launch(ctx, LaunchOptions{Path: path, Home: t.TempDir(), Headless: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		_ = proc.Close(cctx)
	})

	var mu sync.Mutex
	var worker []hook.Event
	sess, err := Attach(ctx, proc.WSURL, SessionOptions{TeamsURL: srv.TeamsURL(), Logger: testLogger()}, Handlers{
		OnBinding: func(transport, payload string) {
			if ev, err := hook.Parse(payload); err == nil && transport == TransportWorker {
				mu.Lock()
				worker = append(worker, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	seen := func(title string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range worker {
			if ev.Kind == hook.KindNotification && ev.Title == title {
				return true
			}
		}
		return false
	}
	waitSeen := func(title string) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !seen(title); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("worker report %q never arrived", title)
			}
		}
	}
	waitSeen("Carol")

	// Inspect the worker from a second session.
	infos, err := chromedp.Targets(sess.pageCtx)
	if err != nil {
		t.Fatal(err)
	}
	var sw target.ID
	for _, ti := range infos {
		if ti.Type == "service_worker" && ti.URL == srv.Origin()+"/v2/sw.js" {
			sw = ti.TargetID
		}
	}
	if sw == "" {
		t.Fatal("no service worker target")
	}
	wctx, wcancel := chromedp.NewContext(sess.pageCtx, chromedp.WithTargetID(sw), chromedp.WithDetachOnCancel())
	defer wcancel()
	eval := func(expr string, out any) {
		t.Helper()
		err := chromedp.Do(wctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			r, err := cdp.Call(ctx, tg, runtime.Evaluate, runtime.EvaluateParams{Expression: expr, ReturnByValue: ptr(true)})
			if err != nil || out == nil || r.Result == nil {
				return err
			}
			return json.Unmarshal(r.Result.Value, out)
		}))
		if err != nil {
			t.Fatalf("evaluate in worker: %v", err)
		}
	}
	var state struct {
		Binding string
		Queued  int
	}
	eval(`({Binding: typeof self.__tbd, Queued: Array.isArray(self.__tbdQ) ? self.__tbdQ.length : -1})`, &state)
	if state.Binding != "function" || state.Queued != -1 {
		t.Errorf("worker state %+v: want the binding installed and the fallback queue never used", state)
	}

	// The fallback: a report that could not use the binding is drained.
	eval(`(self.__tbdQ = [JSON.stringify({kind: "notification", source: "sw-worker", title: "Queued"})], true)`, nil)
	waitSeen("Queued")
}
