package browser

import (
	"strings"
	"testing"
)

func TestMergeDisableFeatures(t *testing.T) {
	ours := []string{"msImplicitSignin"}
	cases := []struct {
		extra []string
		want  string
	}{
		{nil, "--disable-features=msImplicitSignin"},
		{[]string{"--foo"}, "--disable-features=msImplicitSignin|--foo"},
		{[]string{"--disable-features=A,B", "--foo"}, "--disable-features=A,B,msImplicitSignin|--foo"},
		{[]string{"--disable-features=A", "--disable-features=B"}, "--disable-features=A|--disable-features=B,msImplicitSignin"},
		{[]string{"--disable-features="}, "--disable-features=msImplicitSignin"},
	}
	for _, c := range cases {
		if got := strings.Join(mergeDisableFeatures(c.extra, ours), "|"); got != c.want {
			t.Errorf("merge(%q) = %q, want %q", c.extra, got, c.want)
		}
	}
	if got := mergeDisableFeatures([]string{"--x"}, nil); len(got) != 1 || got[0] != "--x" {
		t.Errorf("no features of ours: %q", got)
	}
}
