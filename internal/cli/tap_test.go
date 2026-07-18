package cli

import "testing"

func TestHasSetValue(t *testing.T) {
	cases := []struct {
		sets []string
		key  string
		want bool
	}{
		{nil, "hub.apiToken", false},
		{[]string{"worker.demo=true"}, "hub.apiToken", false},
		{[]string{"hub.apiToken=abc123"}, "hub.apiToken", true},
		{[]string{"worker.demo=true", "hub.apiToken=abc123"}, "hub.apiToken", true},
		// a value that merely contains the key as a substring must not match
		{[]string{"hub.apiTokenExtra=abc123"}, "hub.apiToken", false},
	}
	for _, c := range cases {
		if got := hasSetValue(c.sets, c.key); got != c.want {
			t.Errorf("hasSetValue(%v, %q) = %v, want %v", c.sets, c.key, got, c.want)
		}
	}
}

func TestGenerateAPIToken(t *testing.T) {
	a, err := generateAPIToken()
	if err != nil {
		t.Fatalf("generateAPIToken: %v", err)
	}
	if len(a) != 64 {
		t.Errorf("len(token) = %d, want 64 (32 random bytes hex-encoded)", len(a))
	}
	b, err := generateAPIToken()
	if err != nil {
		t.Fatalf("generateAPIToken: %v", err)
	}
	if a == b {
		t.Error("two calls returned the same token")
	}
}
