package faketeams

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/chromedp/remote"
)

// GUIUnavailable returns why no browser window can open here, or "" if one can: Linux needs a
// display (WSLg, X11 or Wayland); macOS needs a GUI login session, which `launchctl managername`
// reports as "Aqua" (an SSH shell or a headless runner says "Background" or fails); Windows always
// has a desktop for the test's user.
func GUIUnavailable() string {
	switch runtime.GOOS {
	case "windows":
		return ""
	case "darwin":
		out, err := exec.Command("launchctl", "managername").Output()
		if name := strings.TrimSpace(string(out)); err != nil || name != "Aqua" {
			return fmt.Sprintf("not in a GUI (Aqua) session: launchctl managername = %q (err %v)", name, err)
		}
		return ""
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return "no display (DISPLAY and WAYLAND_DISPLAY are unset)"
		}
		return ""
	}
}

// RequireGUI skips a windowed test where GUIUnavailable gives a reason; with TBD_REQUIRE_GUI=1
// (set by the macOS CI legs) it fails instead, so a runner without a GUI session cannot pass the
// windowed tests by skipping them.
func RequireGUI(t testing.TB) {
	t.Helper()
	reason := GUIUnavailable()
	if reason == "" {
		return
	}
	if os.Getenv("TBD_REQUIRE_GUI") == "1" {
		t.Fatalf("TBD_REQUIRE_GUI=1, but a window cannot open here: %s", reason)
	}
	t.Skipf("a windowed test cannot run here: %s", reason)
}

// ClosePageTargets closes every page target (window or tab) of the browser at wsURL with
// Target.closeTarget, as a user closing the windows would. Unlike Browser.close it does not ask
// the browser to quit: on Linux and Windows the browser quits after its last window anyway, on
// macOS it keeps running, and `tbd login` must notice either way. It returns how many pages it
// closed.
func ClosePageTargets(ctx context.Context, wsURL string) (int, error) {
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		return 0, fmt.Errorf("dial %s: %w", wsURL, err)
	}
	var once sync.Once
	closeConn := func() { once.Do(func() { conn.Close() }) }
	defer closeConn()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // Conn.Read does not watch ctx
		select {
		case <-ctx.Done():
			closeConn()
		case <-stop:
		}
	}()

	var id int64
	call := func(method string, params, result any) error {
		id++
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, &cdproto.Message{ID: id, Method: cdproto.MethodType(method), Params: jsonv2.Value(b)}); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		for {
			var m cdproto.Message
			if err := conn.Read(ctx, &m); err != nil {
				return fmt.Errorf("%s: %w", method, err)
			}
			if m.ID != id {
				continue // an event
			}
			if m.Error != nil {
				return fmt.Errorf("%s: %w", method, m.Error)
			}
			if result != nil {
				return json.Unmarshal(m.Result, result)
			}
			return nil
		}
	}

	var res struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := call("Target.getTargets", struct{}{}, &res); err != nil {
		return 0, err
	}
	var pages []string
	for _, ti := range res.TargetInfos {
		if ti.Type == "page" {
			pages = append(pages, ti.TargetID)
		}
	}
	for i, p := range pages {
		err := call("Target.closeTarget", map[string]string{"targetId": p}, nil)
		if err != nil && i < len(pages)-1 {
			return i, err
		}
		// The browser may quit at once after its last page, before it replies.
	}
	return len(pages), nil
}
