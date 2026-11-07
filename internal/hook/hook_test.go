package hook

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseValid(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    Event
	}{
		{
			"window notification",
			`{"kind":"notification","source":"window","title":"Alice","body":"hi","tag":"19:abc@thread.v2","icon":"https://x/i.png","href":"https://teams.microsoft.com/v2/","data":{"id":"1"}}`,
			Event{Kind: "notification", Source: "window", Title: "Alice", Body: "hi", Tag: "19:abc@thread.v2", Icon: "https://x/i.png", Href: "https://teams.microsoft.com/v2/", Data: json.RawMessage(`{"id":"1"}`)},
		},
		{
			"sw-page default-less source kept",
			`{"kind":"notification","source":"sw-page","title":"Bob"}`,
			Event{Kind: "notification", Source: "sw-page", Title: "Bob"},
		},
		{
			"unknown source falls back to window",
			`{"kind":"notification","source":"evil","title":"x"}`,
			Event{Kind: "notification", Source: "window", Title: "x"},
		},
		{
			"missing source defaults to window",
			`{"kind":"hooked","href":"https://teams.microsoft.com/v2/"}`,
			Event{Kind: "hooked", Source: "window", Href: "https://teams.microsoft.com/v2/"},
		},
		{
			"title with count",
			`{"kind":"title","count":3}`,
			Event{Kind: "title", Source: "window", Count: 3},
		},
		{
			"unknown fields ignored",
			`{"kind":"notification","title":"x","evil":{"a":1},"extra":"y"}`,
			Event{Kind: "notification", Source: "window", Title: "x"},
		},
		{
			"null data ignored",
			`{"kind":"notification","title":"x","data":null}`,
			Event{Kind: "notification", Source: "window", Title: "x"},
		},
		{
			"leading whitespace object",
			"  \n\t{\"kind\":\"hooked\"}",
			Event{Kind: "hooked", Source: "window"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.payload)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if got.Kind != tc.want.Kind || got.Source != tc.want.Source || got.Title != tc.want.Title ||
				got.Body != tc.want.Body || got.Tag != tc.want.Tag || got.Icon != tc.want.Icon ||
				got.Href != tc.want.Href || got.Count != tc.want.Count || got.DataDropped != tc.want.DataDropped ||
				string(got.Data) != string(tc.want.Data) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseHostile(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"not json", `not json at all`},
		{"not an object (array)", `[1,2,3]`},
		{"not an object (string)", `"hello"`},
		{"not an object (number)", `42`},
		{"json null", `null`},
		{"empty", ``},
		{"missing kind", `{"title":"x"}`},
		{"unknown kind", `{"kind":"evil","title":"x"}`},
		{"title is a number", `{"kind":"notification","title":123}`},
		{"body is an object", `{"kind":"notification","body":{"a":1}}`},
		{"count is a float", `{"kind":"title","count":3.5}`},
		{"count is negative", `{"kind":"title","count":-1}`},
		{"count is a string", `{"kind":"title","count":"3"}`},
		{"count too big", `{"kind":"title","count":2000000}`},
		{"trailing garbage after object", `{"kind":"hooked"} trailing`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.payload); err == nil {
				t.Fatalf("Parse(%q) accepted a hostile payload", tc.payload)
			}
		})
	}
}

func TestParseTooLarge(t *testing.T) {
	big := `{"kind":"notification","title":"` + strings.Repeat("a", MaxPayloadBytes) + `"}`
	if _, err := Parse(big); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

func TestParseCaps(t *testing.T) {
	title := strings.Repeat("t", maxTitleRunes+50)
	body := strings.Repeat("b", maxBodyRunes+50)
	tag := strings.Repeat("g", maxTagRunes+50)
	icon := "https://x/" + strings.Repeat("i", maxURLBytes)
	href := "https://x/" + strings.Repeat("h", maxURLBytes)
	data := `{"blob":"` + strings.Repeat("d", maxDataBytes) + `"}`
	payload, _ := json.Marshal(map[string]any{
		"kind": "notification", "title": title, "body": body, "tag": tag,
		"icon": icon, "href": href, "data": json.RawMessage(data),
	})
	ev, err := Parse(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(ev.Title) != maxTitleRunes {
		t.Errorf("title not capped: %d", utf8.RuneCountInString(ev.Title))
	}
	if utf8.RuneCountInString(ev.Body) != maxBodyRunes {
		t.Errorf("body not capped: %d", utf8.RuneCountInString(ev.Body))
	}
	if utf8.RuneCountInString(ev.Tag) != maxTagRunes {
		t.Errorf("tag not capped: %d", utf8.RuneCountInString(ev.Tag))
	}
	if ev.Icon != "" {
		t.Errorf("over-cap icon should be dropped, got %d bytes", len(ev.Icon))
	}
	if len(ev.Href) > maxURLBytes {
		t.Errorf("href not capped: %d", len(ev.Href))
	}
	if !ev.DataDropped || ev.Data != nil {
		t.Errorf("over-cap data should be dropped: dropped=%v len=%d", ev.DataDropped, len(ev.Data))
	}
}

func TestParseInvalidUTF8Replaced(t *testing.T) {
	// A JSON string cannot carry a raw invalid byte, but it can carry a lone surrogate escape,
	// which Go's json decodes to the replacement rune. Verify the result is valid UTF-8.
	ev, err := Parse(`{"kind":"notification","title":"a\ud800b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(ev.Title) {
		t.Fatalf("title not valid UTF-8: %q", ev.Title)
	}
}

func TestParseRuneTruncationNoSplit(t *testing.T) {
	// A multi-byte rune right at the cap boundary must not be split.
	title := strings.Repeat("é", maxTitleRunes+10) // 2 bytes each
	payload, _ := json.Marshal(map[string]any{"kind": "notification", "title": title})
	ev, err := Parse(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(ev.Title) {
		t.Fatal("truncation split a UTF-8 sequence")
	}
	if utf8.RuneCountInString(ev.Title) != maxTitleRunes {
		t.Fatalf("got %d runes", utf8.RuneCountInString(ev.Title))
	}
}

func TestDescribeData(t *testing.T) {
	cases := map[string]string{
		`{"chatId":"19:abc@thread.v2","id":"1648741500652","n":3,"ok":true,"arr":[1,2],"obj":{"a":1}}`: "{arr:arr(2),chatId:str(16),id:str(13),n:num,obj:obj(1),ok:bool}",
		``:         "{}",
		`null`:     "{}",
		`"hi"`:     "str(2)",
		`[1,2,3]`:  "arr(3)",
		`not json`: "invalid(8)",
	}
	for in, want := range cases {
		if got := DescribeData(json.RawMessage(in)); got != want {
			t.Errorf("DescribeData(%q) = %q, want %q", in, got, want)
		}
	}
	// Describing data must not reveal id contents, in values or in keys that look like data.
	d := DescribeData(json.RawMessage(`{"chatId":"19:secret-value@thread.v2","alice@contoso.com":1,"8:orgid:secret":true}`))
	if strings.Contains(d, "secret") || strings.Contains(d, "alice") {
		t.Errorf("DescribeData leaked id content: %q", d)
	}
	if d != "{key(14):bool,key(17):num,chatId:str(25)}" {
		t.Errorf("DescribeData = %q", d)
	}
	// At most 32 keys are listed.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%02d":1`, i)
	}
	b.WriteString("}")
	if d := DescribeData(json.RawMessage(b.String())); !strings.HasSuffix(d, ",…+8}") {
		t.Errorf("DescribeData did not cap keys: %q", d)
	}
}

func TestParseDataError(t *testing.T) {
	ev, err := Parse(`{"kind":"notification","title":"x","data":null,"dataError":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.DataDropped || ev.Data != nil {
		t.Fatalf("dataError should mark DataDropped: %+v", ev)
	}
}

func TestShape(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"19:abc@thread.v2", 96, "99:aaa@aaaaaa.a9"},
		{"https://x.example/v1/users/8:orgid:1f2e/profilepicture", 96, "aaaaa://a.aaaaaaa/a9/aaaaa/9:aaaaa:9a9a/aaaaaaaaaaaaaa"},
		{"Zoë Ζωή 李. 😀", 96, "aaa aaa a. ?"},
		{"abcdef", 3, "aaa…(+3)"},
		{"", 10, ""},
	} {
		if got := Shape(tc.in, tc.max); got != tc.want {
			t.Errorf("Shape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseOptionKeys(t *testing.T) {
	ev, err := Parse(`{"kind":"notification","title":"t","optionKeys":["body","tag",7,"data",{"x":1},"` + strings.Repeat("k", 60) + `"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ev.OptionKeys, ","); got != "body,tag,data,"+strings.Repeat("k", 40) {
		t.Errorf("OptionKeys = %q", got)
	}
	for _, bad := range []string{`"nope"`, `{"a":1}`, `null`, `[1,2]`} {
		ev, err := Parse(`{"kind":"notification","optionKeys":` + bad + `}`)
		if err != nil || len(ev.OptionKeys) != 0 {
			t.Errorf("optionKeys %s: %v %v", bad, ev.OptionKeys, err)
		}
	}
	many := make([]string, 50)
	for i := range many {
		many[i] = `"k"`
	}
	ev, _ = Parse(`{"kind":"notification","optionKeys":[` + strings.Join(many, ",") + `]}`)
	if len(ev.OptionKeys) != 32 {
		t.Errorf("%d keys kept, want 32", len(ev.OptionKeys))
	}
}
