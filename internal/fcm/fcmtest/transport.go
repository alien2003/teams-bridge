package fcmtest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Transport returns an http.RoundTripper that serves Google's production URLs
// (https://oauth2.googleapis.com/token and https://fcm.googleapis.com/v1/…) from this server in
// memory, without a socket. Any other URL fails, so a client using it can never reach the real
// Google services. Use it with service accounts from NewServiceAccountFor(GoogleTokenURL) to test
// a client's default endpoints.
func (s *Server) Transport() http.RoundTripper { return memTransport{s} }

type memTransport struct{ s *Server }

var errHost = errors.New("fcmtest: Transport serves only https://" + GoogleFCMHost + "/v1/… and " + GoogleTokenURL)

func (m memTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	host, path := r.URL.Host, r.URL.Path
	ok := r.URL.Scheme == "https" &&
		(host == "oauth2.googleapis.com" && path == "/token" ||
			host == GoogleFCMHost && strings.HasPrefix(path, "/v1/"))
	if !ok {
		return nil, fmt.Errorf("%w; refused %s://%s%s", errHost, r.URL.Scheme, host, path)
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	in := r.Clone(r.Context())
	in.RequestURI = r.URL.RequestURI()
	in.Host = host
	in.RemoteAddr = "192.0.2.1:443"
	rec := httptest.NewRecorder()
	m.s.handler.ServeHTTP(rec, in)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}
