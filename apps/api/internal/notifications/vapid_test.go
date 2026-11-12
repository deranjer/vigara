package notifications

import (
	"context"
	"testing"
)

func TestVAPIDSubject(t *testing.T) {
	for _, c := range []struct{ explicit, public, want string }{
		{"mailto:ops@example.com", "https://vigara.example.com", "mailto:ops@example.com"},
		{"", "https://vigara.example.com/", "https://vigara.example.com"},
		{"not a subject", "http://192.168.1.97:8080", "mailto:vigara@localhost"},
		{"", "", "mailto:vigara@localhost"},
	} {
		if got := VAPIDSubject(c.explicit, c.public); got != c.want {
			t.Errorf("VAPIDSubject(%q,%q)=%q want %q", c.explicit, c.public, got, c.want)
		}
	}
}

func TestLoadVAPIDKeepsOneKey(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	first, err := LoadVAPID(ctx, p, "mailto:a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadVAPID(ctx, p, "https://vigara.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey() == "" || first.PublicKey() != second.PublicKey() {
		t.Fatalf("key changed between loads: %q %q", first.PublicKey(), second.PublicKey())
	}
}
