package serverurl

import "testing"

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		allow bool
		ok    bool
	}{
		{"https://vigira.example", false, true},
		{"https://203.0.113.5:8443", false, true},
		{"http://localhost:8080", false, true},
		{"http://127.0.0.1:8080", false, true},
		{"http://[::1]:8080", false, true},
		{"http://192.168.1.97:8080", false, false},
		{"http://192.168.1.97:8080", true, true},
		{"http://10.0.0.5", true, true},
		{"http://172.16.4.2:8080", true, true},
		{"http://172.32.0.1", true, false},
		{"http://100.101.102.103:8080", true, true},
		{"http://100.128.0.1", true, false},
		{"http://169.254.1.1", true, true},
		{"http://[fd7a:115c:a1e0::1]:8080", true, true},
		{"http://nas:8080", true, true},
		{"http://nas.local:8080", true, true},
		{"http://box.tail1234.ts.net:8080", true, true},
		{"http://vigira.example", true, false},
		{"http://8.8.8.8", true, false},
		{"http://[2001:db8::1]", true, false},
		{"https://user:pass@vigira.example", true, false},
		{"https://vigira.example/vigira", true, true},
		{"https://vigira.example/?x=1", true, false},
		{"ftp://192.168.1.2", true, false},
	} {
		if err := Validate(tc.raw, tc.allow); (err == nil) != tc.ok {
			t.Errorf("Validate(%q, %v) = %v, want ok=%v", tc.raw, tc.allow, err, tc.ok)
		}
	}
}

func TestIsSecure(t *testing.T) {
	for raw, want := range map[string]bool{"https://a.example": true, "http://localhost:1": true, "http://192.168.1.1": false, "http://100.64.0.1": false} {
		if IsSecure(raw) != want {
			t.Errorf("IsSecure(%q) != %v", raw, want)
		}
	}
}
