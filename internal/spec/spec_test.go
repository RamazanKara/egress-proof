package spec

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestErrorLocations(t *testing.T) {
	for _, tt := range []struct {
		name, data, message string
	}{
		{"empty", "", "line 1: spec is empty"},
		{"missing-selector", "reachable: [example.com]\n", "line 1: namespace or podSelector"},
		{"missing-destinations", "namespace: team\n", "line 1: at least one"},
		{"namespace", "# spec\nnamespace: bad/name\nreachable: [example.com]\n", "line 2: invalid namespace"},
		{"selector", "namespace: team\npodSelector: app in (\nreachable: [example.com]\n", "line 2: invalid podSelector"},
		{"destination", "namespace: team\nreachable:\n  - example.com\n  - http://example.com\n", "line 4: reachable[2]:"},
		{"duplicate", "namespace: team\nreachable: [example.com]\nblocked:\n  - example.com\n", "line 4: blocked[1]: duplicate destination"},
		{"flow-sequence", "namespace: team\nblocked: [example.com, example.com]\n", "line 2: blocked[2]: duplicate destination"},
		{"unknown-field", "namespace: team\nreachble: [example.com]\n", "line 2: field reachble"},
		{"wrong-type", "namespace: team\nreachable: {}\n", "line 2: cannot unmarshal"},
		{"duplicate-key", "namespace: team\nnamespace: other\nreachable: [example.com]\n", "line 2: mapping key"},
		{"extra-document", "namespace: team\nreachable: [example.com]\n---\nnamespace: other\n", "line 3: spec must contain exactly one"},
		{"empty-extra-document", "namespace: team\nreachable: [example.com]\n---\n", "line 3: spec must contain exactly one"},
		{"alias", "namespace: &ns bad/name\npodSelector: *ns\nreachable: [example.com]\n", "line 1: invalid namespace"},
		{"merged-field", "<<:\n  namespace: bad/name\n  reachable: [example.com]\n", "line 2: invalid namespace"},
		{"merged-list", "namespace: team\n<<:\n  reachable:\n    - http://example.com\n", "line 4: reachable[1]:"},
		{"merged-precedence", "<<:\n  - <<: {namespace: bad/name}\n  - namespace: team\nreachable: [example.com]\n", "line 2: invalid namespace"},
		{"list-alias", "namespace: team\nreachable: &destinations [example.com]\nblocked: *destinations\n", "line 2: blocked[1]: duplicate destination"},
		{"null-item", "namespace: team\nreachable: [null]\n", "line 1: at least one"},
		{"after-null", "namespace: team\nreachable:\n  - null\n  - http://example.com\n", "line 4: reachable[2]:"},
		{"after-null-alias", "namespace: team\nreachable:\n  - &empty null\n  - *empty\n  - http://example.com\n", "line 5: reachable[3]:"},
		{"alias-key", "namespace: &field reachable\n? *field\n: [example.com, http://example.com]\n", "line 3: reachable[2]:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("got %v, want %q", err, tt.message)
			}
		})
	}
}

func TestAliasesAndMerges(t *testing.T) {
	for _, data := range []string{
		"namespace: team\nreachable: [&host example.com, 'example.com:443']\n",
		"<<: {namespace: team, reachable: [example.com]}\n",
		"<<: [{namespace: team, reachable: [example.com]}, {namespace: other}]\n",
		"namespace: team\n<<: {namespace: bad/name, reachable: [example.com]}\n",
		"namespace: team\nreachable: [null, example.com]\n",
		"<<: {namespace: team, reachable: [http://example.com]}\nreachable: [example.com]\n",
	} {
		t.Run(data, func(t *testing.T) {
			s, _, err := Parse([]byte(data))
			if err != nil || s.Namespace != "team" || s.Reachable[0] != "example.com" {
				t.Fatalf("got %+v, %v", s, err)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "null", "namespace: team\nreachable: [example.com]\n",
		"podSelector: app=api\nblocked: ['[::1]:443']\n",
		"<<: {namespace: team, reachable: [example.com]}\n",
		"namespace: team\nreachable: &r [example.com]\nblocked: *r\n",
		"namespace: team\nreachable: [&empty null, example.com, *empty]\n",
		"&root {<<: *root, namespace: team, reachable: [example.com]}",
		"namespace: team\nreachable: [example.com]\n---\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, hash, err := Parse(data)
		if hash != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
			t.Fatal("hash did not preserve input bytes")
		}
		if err != nil {
			return
		}
		encoded, err := yaml.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, _, err := Parse(encoded)
		if err != nil || roundtrip.Namespace != s.Namespace || roundtrip.PodSelector != s.PodSelector || !slices.Equal(roundtrip.Checks(), s.Checks()) {
			t.Fatalf("spec changed after round trip: %+v, %v", roundtrip, err)
		}
	})
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
