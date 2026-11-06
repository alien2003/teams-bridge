package hook

import (
	"strings"
	"testing"
)

func TestSenderMRI(t *testing.T) {
	for in, want := range map[string]string{
		"https://teams.live.com/api/mt/beta/users/8:live:.cid.0a1b2c3d4e5f6789/profilepicturev2?displayname=B%20B":             "8:live:.cid.0a1b2c3d4e5f6789",
		"https://teams.microsoft.com/api/mt/zz/beta/users/8%3Aorgid%3A1f2e3d4c-0000-1111-2222-333344445555/profilepicturev2": "8:orgid:1f2e3d4c-0000-1111-2222-333344445555",
		"https://example.com/avatar.png":    "",
		"https://x/users/../profilepicture": "",
		"":                                  "",
	} {
		if got := SenderMRI(in); got != want {
			t.Errorf("SenderMRI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribeOccurrences(t *testing.T) {
	mri := "8:orgid:1f2e3d4c-0000-1111-2222-333344445555"
	frame := `3:::{"id":"x1","method":"POST","url":"/v4/f/abc/messaging","body":"{}","resource":{"resourceType":"NewMessage","resource":{"id":"1730900000000","conversationLink":"https://zz.ng.msg.teams.microsoft.com/v1/users/ME/conversations/19:abc@unq.gbl.spaces","from":"https://zz.ng.msg.teams.microsoft.com/v1/users/ME/contacts/` + mri + `","content":"secret text","imdisplayname":"Alice Secret"}}}`
	got := DescribeOccurrences(frame, mri, 3)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	d := got[0]
	for _, want := range []string{"path=resource.resource.from", "keys=", "conversationLink", "conversationLink=aaaaa://", "99:aaa@aaa.aaa.aaaaaa", "parent_keys="} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q: %s", want, d)
		}
	}
	for _, leak := range []string{"secret text", "Alice", "abc@unq", mri, "1730900000000"} {
		if strings.Contains(d, leak) {
			t.Errorf("description leaks %q: %s", leak, d)
		}
	}
	if DescribeOccurrences(frame, "8:orgid:other", 3) != nil {
		t.Error("no hit expected")
	}
	if got := DescribeOccurrences("binary "+mri, mri, 3); len(got) != 1 || !strings.HasPrefix(got[0], "non-json") {
		t.Errorf("non-JSON: %v", got)
	}
}
