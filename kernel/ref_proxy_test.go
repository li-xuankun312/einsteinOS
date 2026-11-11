package kernel

import (
	"strings"
	"testing"
)

// A batched fetch that fails directly is tried again through the machine's proxy, the one it
// names for every protocol (11/11 05:48: ALL_PROXY=socks5://127.0.0.1:1080).
func TestAReferenceFetchIsTriedThroughTheMachinesProxy(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	r := refFetchRoutes()
	if len(r) != 2 || r[0] != "" || r[1] != "socks5h://127.0.0.1:1080" {
		t.Fatalf("routes: %q", r)
	}
	if a := strings.Join(refProxyArgs(r[1]), " "); a != "-c http.proxy=socks5h://127.0.0.1:1080" {
		t.Fatalf("args: %q", a)
	}
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	if r := refFetchRoutes(); len(r) != 1 || r[0] != "" || refProxyArgs("") != nil {
		t.Fatalf("no proxy: %q", r)
	}
}
