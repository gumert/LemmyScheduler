package lemmyaccount

import "testing"

func TestNormalizeInstanceURL(t *testing.T) {
	good := map[string]string{
		"https://lemmy.example":      "https://lemmy.example",
		" https://lemmy.example/ ":   "https://lemmy.example",
		"http://localhost:8536":      "http://localhost:8536",
		"https://Lemmy.example:8443": "https://Lemmy.example:8443",
	}
	for in, want := range good {
		if got, err := NormalizeInstanceURL(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "lemmy.example", "ftp://lemmy.example", "https://", "https://lemmy.example/api",
		"https://user:pw@lemmy.example", "https://lemmy.example?x=1", "https://lemmy.example#f",
	} {
		if got, err := NormalizeInstanceURL(in); err == nil {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
}
