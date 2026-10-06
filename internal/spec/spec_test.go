package spec

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestParse(t *testing.T) {
	data := []byte("namespace: payments\npodSelector: app in (api,worker)\nreachable:\n  - example.com\n  - https://example.com/status\nblocked:\n  - '[2001:db8::1]:443'\n")
	s, hash, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if hash != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) || s.Namespace != "payments" || len(s.Checks()) != 3 {
		t.Fatalf("unexpected spec/hash: %+v %s", s, hash)
	}
	if s.Checks()[2].Expected != "blocked" {
		t.Fatal("lost blocked expectation")
	}
	_, changed, err := Parse(append(data, '\n'))
	if err != nil || hash == changed {
		t.Fatal("hash must cover the exact input bytes")
	}
}

func TestRejectInvalidSpec(t *testing.T) {
	for _, data := range []string{
		"", "namespace: default", "reachable: [example.com]",
		"namespace: default\nreachble: [example.com]",
		"namespace: bad/name\nreachable: [example.com]",
		"namespace: default\npodSelector: app in (\nreachable: [example.com]",
		"namespace: default\nreachable: [example.com]\nblocked: [example.com]",
		"namespace: default\nreachable: [example.com, example.com]",
		"namespace: default\nnamespace: other\nreachable: [example.com]",
		"namespace: default\nreachable: [http://example.com]",
		"namespace: default\nreachable: [example.com]\n---\nnamespace: other",
		"namespace: default\nreachable: [example.com]\n---\n",
	} {
		t.Run(data, func(t *testing.T) {
			if _, _, err := Parse([]byte(data)); err == nil {
				t.Fatal("accepted invalid spec")
			}
		})
	}
}

func TestNamespaceOrSelector(t *testing.T) {
	for _, data := range []string{
		"namespace: payments\nreachable: [example.com]",
		"podSelector: app=api\nblocked: ['10.0.0.1:443']",
	} {
		if _, _, err := Parse([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
}
