package faketeams

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestServerURLs(t *testing.T) {
	s := New()
	defer s.Close()
	if !strings.HasPrefix(s.TeamsURL(), s.Origin()+"/v2/") {
		t.Errorf("TeamsURL %q not under Origin %q", s.TeamsURL(), s.Origin())
	}
	if s.LoginHost() != "localhost" {
		t.Errorf("LoginHost = %q", s.LoginHost())
	}
	if !strings.HasPrefix(s.LoginURL(), "http://localhost:") || !strings.HasSuffix(s.LoginURL(), "/login") {
		t.Errorf("LoginURL = %q", s.LoginURL())
	}
	if s.APIHostPattern() != "127.0.0.1" {
		t.Errorf("APIHostPattern = %q", s.APIHostPattern())
	}
}

func TestServeIndexAndAssets(t *testing.T) {
	s := New()
	defer s.Close()

	resp, body := get(t, s.TeamsURL())
	if resp.StatusCode != 200 || !strings.Contains(body, "app.js") {
		t.Fatalf("index: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Accept-CH") == "" || resp.Header.Get("Critical-CH") == "" {
		t.Error("index should send Accept-CH and Critical-CH")
	}

	resp, body = get(t, s.Origin()+"/v2/app.js")
	if resp.StatusCode != 200 || !strings.Contains(body, "Notification") {
		t.Fatalf("app.js: %d", resp.StatusCode)
	}
	resp, _ = get(t, s.Origin()+"/v2/sw.js")
	if resp.StatusCode != 200 || resp.Header.Get("Service-Worker-Allowed") != "/v2/" {
		t.Fatalf("sw.js: %d allowed=%q", resp.StatusCode, resp.Header.Get("Service-Worker-Allowed"))
	}

	resp, body = get(t, "http://localhost:"+s.port+"/login")
	if resp.StatusCode != 200 || !strings.Contains(body, "loginfmt") {
		t.Fatalf("login page: %d", resp.StatusCode)
	}
}

func TestExpire401(t *testing.T) {
	s := New()
	defer s.Close()
	poll := s.Origin() + "/api/v1/poll"

	if resp, _ := get(t, poll); resp.StatusCode != 200 {
		t.Fatalf("poll before expiry: %d", resp.StatusCode)
	}
	s.Expire401(2)
	for i := 0; i < 2; i++ {
		if resp, _ := get(t, poll); resp.StatusCode != 401 {
			t.Fatalf("poll %d during expiry: %d, want 401", i, resp.StatusCode)
		}
	}
	if resp, _ := get(t, poll); resp.StatusCode != 200 {
		t.Fatalf("poll after expiry window: %d, want 200", resp.StatusCode)
	}
}

func TestRedirectToLogin(t *testing.T) {
	s := New()
	defer s.Close()
	nav := s.Origin() + "/v2/nav-check"

	if _, body := get(t, nav); strings.Contains(body, "redirect") {
		t.Fatalf("nav-check should not redirect by default: %q", body)
	}
	s.RedirectToLogin()
	resp, body := get(t, nav)
	if resp.StatusCode != 200 || !strings.Contains(body, s.LoginURL()) {
		t.Fatalf("nav-check after RedirectToLogin: %q", body)
	}
	// Only once.
	if _, body := get(t, nav); strings.Contains(body, "redirect") {
		t.Fatalf("redirect should fire once: %q", body)
	}
}

func TestReportRecorded(t *testing.T) {
	s := New()
	defer s.Close()
	payload := `{"type":"env","ua":"x","hidden":true,"visibilityState":"hidden","hasFocus":false,"permission":"granted","highEntropy":{"platformVersion":"19.0.0"}}`
	resp, err := http.Post(s.Origin()+"/report", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	reps := s.Reports()
	if len(reps) != 1 || reps[0].Type != "env" || !reps[0].Hidden || reps[0].Permission != "granted" {
		t.Fatalf("report not recorded: %+v", reps)
	}
	if reps[0].HighEntropy["platformVersion"] != "19.0.0" {
		t.Errorf("high entropy not parsed: %+v", reps[0].HighEntropy)
	}
}

func TestRequestsRecorded(t *testing.T) {
	s := New()
	defer s.Close()
	get(t, s.TeamsURL())
	reqs := s.Requests()
	if len(reqs) == 0 || reqs[0].Path != "/v2/" {
		t.Fatalf("requests: %+v", reqs)
	}
	s.Reset()
	if len(s.Requests()) != 0 || len(s.Reports()) != 0 {
		t.Error("Reset did not clear state")
	}
}

func TestNotifyCommandAndRaised(t *testing.T) {
	s := New()
	defer s.Close()
	nav := s.Origin() + "/v2/nav-check"

	s.Notify("Dave", "on demand", "19:d@thread.v2", map[string]any{"messageId": "1648741501000"})
	s.RedirectToLogin()
	_, body := get(t, nav)
	var cmd struct {
		Notify []struct {
			Title   string         `json:"title"`
			Options map[string]any `json:"options"`
		} `json:"notify"`
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal([]byte(body), &cmd); err != nil {
		t.Fatalf("command %q: %v", body, err)
	}
	if len(cmd.Notify) != 1 || cmd.Notify[0].Title != "Dave" || cmd.Notify[0].Options["body"] != "on demand" ||
		cmd.Notify[0].Options["tag"] != "19:d@thread.v2" || cmd.Redirect != s.LoginURL() {
		t.Fatalf("command = %+v", cmd)
	}
	if _, body := get(t, nav); body != "{}" {
		t.Fatalf("queued commands must be delivered once, got %q", body)
	}

	// WaitRaised wakes up on reports, without polling.
	go func() {
		time.Sleep(50 * time.Millisecond)
		for _, r := range InitialNotifications {
			b, _ := json.Marshal(map[string]string{"type": "raised", "source": r.Source, "title": r.Title})
			resp, err := http.Post(s.Origin()+"/report", "application/json", bytes.NewReader(b))
			if err == nil {
				resp.Body.Close()
			}
		}
	}()
	got, ok := s.WaitRaised(5*time.Second, len(InitialNotifications))
	if !ok || len(got) != len(InitialNotifications) || got[2] != InitialNotifications[2] {
		t.Fatalf("WaitRaised = %v, %v", got, ok)
	}
	if _, ok := s.WaitRaised(100*time.Millisecond, 4); ok {
		t.Fatal("WaitRaised(4) should time out")
	}
	s.Notify("x", "", "", nil)
	s.Reset()
	if len(s.Raised()) != 0 {
		t.Error("Reset kept raised notifications")
	}
	if _, body := get(t, nav); body != "{}" {
		t.Errorf("Reset kept queued notifications: %q", body)
	}
}

func TestReportRejectsGet(t *testing.T) {
	s := New()
	defer s.Close()
	if resp, _ := get(t, s.Origin()+"/report"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET /report = %d", resp.StatusCode)
	}
	if len(s.Reports()) != 0 {
		t.Fatal("a GET was recorded as a report")
	}
}
