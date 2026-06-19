package serviceregistry

import "testing"

func TestIsLoopbackEndpoint(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://127.0.0.1:8080", true},
		{"http://localhost:8080", true},
		{"localhost:4433", true},
		{"127.0.0.1:4433", true},
		{"https://api.example.com", false},
		{"api.example.com:443", false},
	}
	for _, c := range cases {
		if got := IsLoopbackEndpoint(c.in); got != c.want {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestFilterEndpointsNonDev(t *testing.T) {
	t.Setenv("CC_DEV_CLUSTER", "")
	addrs := FilterEndpoints([]string{
		"http://127.0.0.1:8080",
		"https://api.ccim.top",
		"http://localhost:8081",
	})
	if len(addrs) != 1 || addrs[0] != "https://api.ccim.top" {
		t.Fatalf("got %v", addrs)
	}
}
