package hook

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/alien2003/teams-bridge/internal/config"
)

// Teams conversation ids and message ids (BRIEF §5 "Deep link").
//
// Conversation ids look like "19:<opaque>@thread.v2" (group and meeting chats),
// "19:<opaque>@unq.gbl.spaces" (1:1 chats), "48:notes" (the self chat), and "19:<opaque>@thread.tacv2"
// or "@thread.skype" (channels). Message ids are the 13-digit millisecond timestamps Teams assigns
// (e.g. 1648741500652); the 19-digit clientmessageid is a different, client-side value.
var (
	threadIDRE  = regexp.MustCompile(`(?:19:[A-Za-z0-9._~%+=\-]+@(?:thread\.v2|thread\.tacv2|thread\.skype|unq\.gbl\.spaces)|48:notes)`)
	messageIDRE = regexp.MustCompile(`\b[0-9]{13}\b`)
	// conversationRE is a whole conversation id, as the hook reports it from the page.
	conversationRE = regexp.MustCompile(`^(?:19:[A-Za-z0-9._~%+=\-]+@(?:thread\.v2|thread\.tacv2|thread\.skype|unq\.gbl\.spaces)|48:notes)$`)
)

// DeepLink builds an in-scope Teams URL for a notification, on the origin of teamsURL. It looks in
// the notification tag and data for a Teams conversation id and an optional message id and, using
// the deep-link forms documented at
// https://learn.microsoft.com/microsoftteams/platform/concepts/build-and-test/deep-link-teams,
// returns:
//
//   - <origin>/l/message/<chatId>/<messageId>?context={"contextType":"chat"}  chat + message id;
//   - <origin>/l/message/<channelId>/<messageId>                             channel + message id;
//   - <origin>/l/chat/<chatId>/conversations?context={"contextType":"chat"}  chat id only,
//
// with the id percent-encoded (':' → %3A, '@' → %40) and the query encoded, exactly as Teams' own
// "Copy link" writes it.
//
// A channel id without a message id has no documented link that works without the team's groupId,
// so it, like "nothing usable" or an unparsable teamsURL, yields teamsURL unchanged. The result is
// always an http(s) URL on the teamsURL origin. A data key named messageId (any case, any depth)
// wins over the first 13-digit number found. The mapping from real Teams notification data to
// these ids must be verified against live Teams; see docs/DECISIONS.md.
func DeepLink(teamsURL string, ev Event) string {
	base, err := url.Parse(teamsURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return teamsURL
	}
	// Build on the origin the page actually lives on when that is one of the Teams origins (Teams
	// moves users between them; a personal account ends up on teams.live.com), never on another.
	if o, ok := teamsOrigin(ev.Href, base); ok {
		base = o
	}

	hay := ev.Tag
	if len(ev.Data) > 0 {
		hay += "\n" + string(ev.Data)
	}
	// The conversation the hook resolved from the page wins: Teams v2 passes no id with the
	// notification itself (measured, docs/DECISIONS.md D17).
	thread := ev.Conversation
	if thread == "" {
		thread = threadIDRE.FindString(hay)
	}
	if thread == "" {
		return teamsURL
	}
	chat := !strings.HasSuffix(thread, "@thread.tacv2") && !strings.HasSuffix(thread, "@thread.skype")
	msg := messageIDFromData(ev.Data)
	if msg == "" {
		msg = findMessageID(hay, thread)
	}

	// The exact form Teams' own "Copy link" produces (work account, 2026-11): the id with ':' and '@'
	// percent-encoded, and the chat context for chats, e.g.
	// https://teams.cloud.microsoft/l/chat/19%3A…%40thread.v2/conversations?context=%7B%22contextType%22%3A%22chat%22%7D
	origin := base.Scheme + "://" + base.Host
	ctx := "?context=" + url.QueryEscape(`{"contextType":"chat"}`)
	switch {
	case msg != "" && chat:
		return origin + "/l/message/" + escapeID(thread) + "/" + msg + ctx
	case msg != "":
		return origin + "/l/message/" + escapeID(thread) + "/" + msg
	case chat:
		return origin + "/l/chat/" + escapeID(thread) + "/conversations" + ctx
	}
	return teamsURL
}

// escapeID percent-encodes a conversation id as a path segment, ':' and '@' included.
func escapeID(id string) string {
	return strings.NewReplacer(":", "%3A", "@", "%40").Replace(url.PathEscape(id))
}

// teamsOrigin returns the origin of href if it is the teams_url origin or one of the well-known Teams
// origins (config.DefaultPermissionOrigins).
func teamsOrigin(href string, base *url.URL) (*url.URL, bool) {
	u, err := url.Parse(href)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, false
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	ok := origin == strings.ToLower(base.Scheme+"://"+base.Host)
	for _, o := range config.DefaultPermissionOrigins {
		ok = ok || origin == o
	}
	if !ok {
		return nil, false
	}
	return &url.URL{Scheme: strings.ToLower(u.Scheme), Host: strings.ToLower(u.Host)}, true
}

// messageIDFromData returns the value of the first key named messageId (case-insensitive, keys
// visited in sorted order, up to four levels deep) that holds a 13-digit id, as a string or number.
func messageIDFromData(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return ""
	}
	var walk func(v any, depth int) string
	walk = func(v any, depth int) string {
		if depth > 4 {
			return ""
		}
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if strings.EqualFold(k, "messageId") {
					var id string
					switch val := x[k].(type) {
					case string:
						id = val
					case json.Number:
						id = val.String()
					}
					if len(id) == 13 && messageIDRE.MatchString(id) {
						return id
					}
				}
			}
			for _, k := range keys {
				if id := walk(x[k], depth+1); id != "" {
					return id
				}
			}
		case []any:
			for _, e := range x {
				if id := walk(e, depth+1); id != "" {
					return id
				}
			}
		}
		return ""
	}
	return walk(v, 0)
}

// findMessageID returns the first 13-digit number in hay that is not part of the thread id.
func findMessageID(hay, thread string) string {
	for _, m := range messageIDRE.FindAllString(hay, -1) {
		if !strings.Contains(thread, m) {
			return m
		}
	}
	return ""
}

// DescribeData renders the shape of a notification's options.data for a privacy-friendly info log:
// the top-level keys with each value's type and length (string contents are never shown; keys that
// look like ids or addresses are masked too; at most 32 keys). The daemon logs the raw data only at
// debug level. For example `{chatId:str(21),id:str(13),mentions:arr(2),meta:obj(3)}`.
func DescribeData(data json.RawMessage) string {
	if len(data) == 0 || isJSONNull(data) {
		return "{}"
	}
	var top any
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Sprintf("invalid(%d)", len(data))
	}
	switch v := top.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for i, k := range keys {
			if i == maxDescribedKeys {
				parts = append(parts, fmt.Sprintf("…+%d", len(keys)-i))
				break
			}
			parts = append(parts, describeKey(k)+":"+describeValue(v[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return describeValue(top)
	}
}

// maxDescribedKeys caps how many keys DescribeData lists.
const maxDescribedKeys = 32

// describeKey keeps ordinary key names (they are what the user needs to refine the deep-link
// mapping) but masks keys that look like data themselves: ids, addresses, long strings.
func describeKey(k string) string {
	if len(k) > 40 || strings.ContainsAny(k, "@: /") {
		return fmt.Sprintf("key(%d)", len(k))
	}
	return k
}

func describeValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "num"
	case string:
		return fmt.Sprintf("str(%d)", len(x))
	case []any:
		return fmt.Sprintf("arr(%d)", len(x))
	case map[string]any:
		return fmt.Sprintf("obj(%d)", len(x))
	default:
		return "?"
	}
}
