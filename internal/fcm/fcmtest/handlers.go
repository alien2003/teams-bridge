package fcmtest

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

const (
	maxRequestBody = 64 << 10
	ttlDefault     = "3600s"
	ttlHeartbeat   = "21600s"

	msgMissingAuth = "Request is missing required authentication credential. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
	msgInvalidAuth = "Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
	msgJWTTime     = "Invalid JWT: Token must be a short-lived token (60 minutes) and in a reasonable timeframe. Check your iat and exp values in the JWT claim."
	msgJWTSig      = "Invalid JWT Signature."
)

var topicRE = regexp.MustCompile(`^[a-zA-Z0-9\-_.~%]{1,900}$`)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/token":
		s.handleToken(w, r)
	case strings.HasPrefix(p, "/v1/projects/") && strings.HasSuffix(p, "/messages:send"):
		s.handleSend(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "/v1/projects/"), "/messages:send"))
	default:
		writeGoogleError(w, Failure{HTTPStatus: http.StatusNotFound, ErrorCode: "-",
			Message: "fcmtest: unknown path " + p})
	}
}

// ---- OAuth token endpoint ----

func writeOAuthError(w http.ResponseWriter, status int, code, desc, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.TokenRequests++
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", s.reject("token: method %s", r.Method), "")
		return
	}
	if len(s.tokenFails) > 0 {
		f := s.tokenFails[0]
		s.tokenFails = s.tokenFails[1:]
		if f.HTTPStatus == 0 {
			f.HTTPStatus = http.StatusBadRequest
		}
		if f.Error == "" {
			f.Error = "invalid_grant"
		}
		if f.Description == "" && f.Error == "invalid_grant" {
			f.Description = msgJWTSig
		}
		writeOAuthError(w, f.HTTPStatus, f.Error, f.Description, f.RetryAfter)
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			s.reject("token: Content-Type %q, want application/x-www-form-urlencoded", r.Header.Get("Content-Type")), "")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(raw) > maxRequestBody {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", s.reject("token: body unreadable or too large"), "")
		return
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", s.reject("token: malformed form: %v", err), "")
		return
	}
	for k, v := range form {
		if (k != "grant_type" && k != "assertion") || len(v) != 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", s.reject("token: unexpected form field %q (×%d)", k, len(v)), "")
			return
		}
	}
	if gt := form.Get("grant_type"); gt != GrantType {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", s.reject("Invalid grant_type: %s", gt), "")
		return
	}
	acc, code, desc := s.verifyJWT(form.Get("assertion"))
	if acc == nil {
		writeOAuthError(w, http.StatusBadRequest, code, s.reject("%s", desc), "")
		return
	}
	tok := randomToken()
	s.tokens[tok] = grant{email: acc.email, exp: s.now().Add(s.tokenTTL)}
	s.issued = append(s.issued, tok)
	s.stats.TokensIssued++
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok, "expires_in": int64(s.tokenTTL / time.Second), "token_type": "Bearer"})
}

var strictB64URL = base64.RawURLEncoding.Strict()

// verifyJWT validates the assertion. On failure it returns the OAuth error code and description.
// Called with s.mu held.
func (s *Server) verifyJWT(a string) (*account, string, string) {
	bad := func(format string, args ...any) (*account, string, string) {
		return nil, "invalid_grant", "Invalid JWT: " + fmt.Sprintf(format, args...)
	}
	if a == "" {
		return nil, "invalid_request", "Missing required parameter: assertion"
	}
	parts := strings.Split(a, ".")
	if len(parts) != 3 {
		return bad("want 3 dot-separated parts, got %d", len(parts))
	}
	var dec [3][]byte
	for i, p := range parts {
		b, err := strictB64URL.DecodeString(p)
		if err != nil || p == "" {
			return bad("part %d is not unpadded base64url", i+1)
		}
		dec[i] = b
	}
	hdr, err := strictObject(dec[0], []string{"alg", "typ", "kid"}, []string{"alg", "typ", "kid"})
	if err != nil {
		return bad("header: %v", err)
	}
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		return bad("header alg=%v typ=%v, want RS256/JWT", hdr["alg"], hdr["typ"])
	}
	claims, err := strictObject(dec[1], []string{"iss", "scope", "aud", "iat", "exp"}, []string{"iss", "scope", "aud", "iat", "exp"})
	if err != nil {
		return bad("claims: %v", err)
	}
	iss, _ := claims["iss"].(string)
	scope, _ := claims["scope"].(string)
	aud, _ := claims["aud"].(string)
	iat, okIat := intClaim(claims["iat"])
	exp, okExp := intClaim(claims["exp"])
	if iss == "" || scope == "" || aud == "" || !okIat || !okExp {
		return bad("claims iss/scope/aud must be non-empty strings and iat/exp integers")
	}
	acc := s.accounts[iss]
	if acc == nil {
		return nil, "invalid_grant", "Invalid grant: account not found"
	}
	if kid, _ := hdr["kid"].(string); kid != acc.kid {
		return nil, "invalid_grant", msgJWTSig + " (unknown kid)"
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(acc.pub, crypto.SHA256, sum[:], dec[2]) != nil {
		return nil, "invalid_grant", msgJWTSig
	}
	if aud != acc.tokenURI {
		return bad("Failed audience check. The expected value was %s", acc.tokenURI)
	}
	if !slices.Contains(strings.Fields(scope), ScopeFCM) && !slices.Contains(strings.Fields(scope), ScopeCloud) {
		return nil, "invalid_scope", "Invalid OAuth scope or ID token audience provided."
	}
	now := s.now()
	switch {
	case exp <= iat, time.Duration(exp-iat)*time.Second > MaxJWTLifetime,
		now.Unix() >= exp, time.Unix(iat, 0).After(now.Add(ClockSkew)):
		return nil, "invalid_grant", msgJWTTime
	}
	return acc, "", ""
}

// strictObject decodes a JSON object whose keys must all be in allowed and include required,
// without duplicates.
func strictObject(b []byte, allowed, required []string) (map[string]any, error) {
	if err := noDuplicateKeys(b); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil || m == nil {
		return nil, errors.New("not a JSON object")
	}
	if d.More() {
		return nil, errors.New("trailing data")
	}
	for k := range m {
		if !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("unexpected field %q", k)
		}
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("missing field %q", k)
		}
	}
	return m, nil
}

func intClaim(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// noDuplicateKeys walks the JSON tokens and fails on a key that appears twice in one object
// (encoding/json would silently keep the last one).
func noDuplicateKeys(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	type frame struct {
		obj    bool
		keys   map[string]bool
		expKey bool
	}
	var stack []*frame
	for {
		tok, err := d.Token()
		if err == io.EOF && len(stack) == 0 {
			return nil
		}
		if err != nil {
			return errors.New("malformed JSON")
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.expKey {
			if k, ok := tok.(string); ok {
				if top.keys[k] {
					return fmt.Errorf("duplicate key %q", k)
				}
				top.keys[k] = true
				top.expKey = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{obj: true, keys: map[string]bool{}, expKey: true})
			continue
		case json.Delim('['):
			stack = append(stack, &frame{})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
		// A value (scalar or a closed container) was completed: the parent object wants a key next.
		if len(stack) > 0 && stack[len(stack)-1].obj {
			stack[len(stack)-1].expKey = true
		}
	}
}

// ---- FCM send endpoint ----

// DefaultFailure fills f's zero fields with what FCM returns for f.HTTPStatus.
func DefaultFailure(f Failure) Failure {
	type def struct{ status, code, msg string }
	defs := map[int]def{
		400: {"INVALID_ARGUMENT", "INVALID_ARGUMENT", "Request contains an invalid argument."},
		401: {"UNAUTHENTICATED", "-", msgInvalidAuth},
		403: {"PERMISSION_DENIED", "SENDER_ID_MISMATCH", "SenderId mismatch"},
		404: {"NOT_FOUND", "UNREGISTERED", "Requested entity was not found."},
		429: {"RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED", "Topic quota exceeded."},
		500: {"INTERNAL", "INTERNAL", "Internal error encountered."},
		503: {"UNAVAILABLE", "UNAVAILABLE", "The service is currently unavailable."},
		504: {"DEADLINE_EXCEEDED", "-", "The request timed out."},
	}
	d, ok := defs[f.HTTPStatus]
	if !ok {
		d = def{"UNKNOWN", "-", http.StatusText(f.HTTPStatus)}
	}
	if f.HTTPStatus == http.StatusBadGateway && f.RawBody == "" && f.Status == "" {
		f.RawBody = "<!DOCTYPE html>\n<html lang=en><meta charset=utf-8><title>Error 502 (Server Error)!!1</title>" +
			"<p><b>502.</b> <ins>That’s an error.</ins><p>The server encountered a temporary error and could not complete your request.<p>Please try again in 30 seconds.  <ins>That’s all we know.</ins></html>\n"
	}
	if f.Status == "" {
		f.Status = d.status
	}
	if f.ErrorCode == "" {
		f.ErrorCode = d.code
	}
	if f.Message == "" {
		f.Message = d.msg
	}
	return f
}

// GoogleErrorJSON is the body FCM sends for f.
func GoogleErrorJSON(f Failure) []byte {
	f = DefaultFailure(f)
	e := map[string]any{"code": f.HTTPStatus, "message": f.Message, "status": f.Status}
	if f.ErrorCode != "-" {
		e["details"] = []any{map[string]string{"@type": FCMErrorType, "errorCode": f.ErrorCode}}
	}
	b, _ := json.MarshalIndent(map[string]any{"error": e}, "", "  ")
	return append(b, '\n')
}

func writeGoogleError(w http.ResponseWriter, f Failure) {
	f = DefaultFailure(f)
	if f.RetryAfter != "" {
		w.Header().Set("Retry-After", f.RetryAfter)
	}
	if f.HTTPStatus == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://accounts.google.com/", error="invalid_token"`)
	}
	if f.RawBody != "" {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(f.HTTPStatus)
		_, _ = io.WriteString(w, f.RawBody)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(f.HTTPStatus)
	_, _ = w.Write(GoogleErrorJSON(f))
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.SendRequests++
	if r.Method != http.MethodPost {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusMethodNotAllowed, ErrorCode: "-",
			Message: s.reject("send: method %s", r.Method)})
		return
	}
	// Authentication comes first, as at Google's front end.
	auth := r.Header.Get("Authorization")
	tok, isBearer := strings.CutPrefix(auth, "Bearer ")
	if auth == "" {
		s.reject("send: no Authorization header")
		writeGoogleError(w, Failure{HTTPStatus: http.StatusUnauthorized, Message: msgMissingAuth})
		return
	}
	g, known := s.tokens[tok]
	if !isBearer || !known || !s.now().Before(g.exp) {
		s.reject("send: invalid or expired bearer token")
		writeGoogleError(w, Failure{HTTPStatus: http.StatusUnauthorized})
		return
	}
	if project != s.ProjectID {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusForbidden, ErrorCode: "-", Message: s.reject(
			"Permission 'cloudmessaging.messages.create' denied on resource '//cloudresourcemanager.googleapis.com/projects/%s' (or it may not exist).", project)})
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusBadRequest, Message: s.reject(
			"Invalid JSON payload received. Content-Type %q, want application/json", r.Header.Get("Content-Type"))})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(raw) > maxRequestBody {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusBadRequest, Message: s.reject("send: body unreadable or too large")})
		return
	}
	m, why := validateSendBody(raw)
	if why != "" {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusBadRequest, Message: s.reject("%s", why)})
		return
	}
	if len(s.sendFails) > 0 {
		f := s.sendFails[0]
		s.sendFails = s.sendFails[1:]
		writeGoogleError(w, f)
		return
	}
	if f, ok := s.topicFails[m.Topic]; ok {
		writeGoogleError(w, f)
		return
	}
	if s.dataLimit > 0 && len("c")+len(m.C) > s.dataLimit {
		writeGoogleError(w, Failure{HTTPStatus: http.StatusBadRequest, Message: s.reject(
			"Message payload is %d bytes; the limit for topic messages is %d.", len("c")+len(m.C), s.dataLimit)})
		return
	}
	m.Name = "projects/" + project + "/messages/" + randomMessageID()
	m.Body = raw
	m.Received = s.now()
	s.decrypt(&m)
	s.record(m)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": m.Name})
}

// decrypt fills Type, Decrypted and DecryptErr. Called with s.mu held.
func (s *Server) decrypt(m *Message) {
	if m.Heartbeat {
		m.Type = proto.TypeHB
	} else {
		m.Type = proto.TypeMsg
	}
	if s.key == nil {
		return
	}
	dm, err := proto.DecryptMessage(*s.key, m.C)
	if err != nil {
		m.DecryptErr = fmt.Errorf("fcmtest: c does not decrypt with the configured key: %w", err)
		return
	}
	m.Decrypted, m.Type = &dm, dm.T
	if (dm.T == proto.TypeHB) != m.Heartbeat {
		m.DecryptErr = fmt.Errorf("fcmtest: type %q sent with ttl %s (BRIEF §3: 21600s for hb, 3600s otherwise)", dm.T, m.TTL)
	}
}

// validateSendBody checks the exact §3 shape:
// {"message":{"topic":T,"android":{"priority":"high","ttl":"3600s"|"21600s"},"data":{"c":C}}}.
func validateSendBody(raw []byte) (Message, string) {
	var m Message
	if err := noDuplicateKeys(raw); err != nil {
		return m, "Invalid JSON payload received. " + err.Error()
	}
	var top map[string]json.RawMessage
	if why := decodeExact(raw, &top, "request", []string{"message"}); why != "" {
		return m, why
	}
	var msg map[string]json.RawMessage
	if why := decodeExact(top["message"], &msg, "message", []string{"topic", "android", "data"}); why != "" {
		return m, why
	}
	if err := json.Unmarshal(msg["topic"], &m.Topic); err != nil {
		return m, "Invalid value at 'message.topic': want a string"
	}
	if strings.HasPrefix(m.Topic, "/topics/") {
		return m, "Invalid value at 'message.topic': the \"/topics/\" prefix must not be provided"
	}
	if !topicRE.MatchString(m.Topic) {
		return m, fmt.Sprintf("Invalid value at 'message.topic' (%q): must match [a-zA-Z0-9-_.~%%]{1,900}", m.Topic)
	}
	var android map[string]json.RawMessage
	if why := decodeExact(msg["android"], &android, "message.android", []string{"priority", "ttl"}); why != "" {
		return m, why
	}
	if json.Unmarshal(android["priority"], &m.Priority) != nil || m.Priority != "high" {
		return m, fmt.Sprintf("fcmtest: message.android.priority must be the string \"high\" (BRIEF §3), got %s", android["priority"])
	}
	if json.Unmarshal(android["ttl"], &m.TTL) != nil || (m.TTL != ttlDefault && m.TTL != ttlHeartbeat) {
		return m, fmt.Sprintf("fcmtest: message.android.ttl must be \"3600s\" or \"21600s\" (BRIEF §3), got %s", android["ttl"])
	}
	m.Heartbeat = m.TTL == ttlHeartbeat
	var data map[string]json.RawMessage
	if why := decodeExact(msg["data"], &data, "message.data", []string{"c"}); why != "" {
		return m, why
	}
	if json.Unmarshal(data["c"], &m.C) != nil {
		return m, "Invalid value at 'message.data[c]': want a string"
	}
	if n := len("c") + len(m.C); n > proto.FCMDataLimit {
		return m, fmt.Sprintf("Message payload is %d bytes; the limit is %d.", n, proto.FCMDataLimit)
	}
	sealed, err := base64.StdEncoding.Strict().DecodeString(m.C)
	if err != nil {
		return m, "fcmtest: message.data.c is not standard padded base64"
	}
	m.SealedLen = len(sealed)
	if l := len(sealed) - proto.NonceSize - proto.TagSize; l != proto.SmallPad && l != proto.LargePad {
		return m, fmt.Sprintf("fcmtest: message.data.c decodes to %d bytes, want %d or %d (BRIEF §3)",
			len(sealed), proto.NonceSize+proto.SmallPad+proto.TagSize, proto.NonceSize+proto.LargePad+proto.TagSize)
	}
	return m, ""
}

// decodeExact decodes an object whose key set must equal want; it names extra and missing keys.
func decodeExact(raw json.RawMessage, into *map[string]json.RawMessage, where string, want []string) string {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(into); err != nil || *into == nil {
		return fmt.Sprintf("Invalid JSON payload received. '%s' must be a JSON object", where)
	}
	if d.More() {
		return "Invalid JSON payload received. Unexpected token after the object."
	}
	for k := range *into {
		if !slices.Contains(want, k) {
			switch {
			case where == "message" && (k == "token" || k == "condition" || k == "fid"):
				return fmt.Sprintf("fcmtest: message.%s: tbd sends to a topic only, never to a %s (BRIEF §2)", k, k)
			case where == "message" && k == "notification":
				return "fcmtest: message.notification: tbd sends data messages only (BRIEF §3)"
			}
			return fmt.Sprintf("Invalid JSON payload received. Unknown name %q at '%s': Cannot find field (or not allowed by BRIEF §3).", k, where)
		}
	}
	for _, k := range want {
		if _, ok := (*into)[k]; !ok {
			return fmt.Sprintf("fcmtest: '%s.%s' is required (BRIEF §3)", where, k)
		}
	}
	return ""
}
