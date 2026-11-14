package kernel

// ref_proxy.go — the reference clone's network fetches through the machine's proxy.
//
// 11/10 13:27 and 11/11 05:48 the batched blob fetch of the reference failed on GitHub's TLS
// ("gnutls_handshake() failed") when made directly; the same fetch through the machine's
// socks proxy (ALL_PROXY, 127.0.0.1:1080) succeeded when it was run by hand.

import (
	"os"
	"strings"
)

// refProxy: the proxy the machine names for every protocol (ALL_PROXY / all_proxy), with
// socks5 turned into socks5h so the proxy resolves github.com; "" when none is set.
func refProxy() string {
	p := os.Getenv("ALL_PROXY")
	if p == "" {
		p = os.Getenv("all_proxy")
	}
	if strings.HasPrefix(p, "socks5://") {
		p = "socks5h://" + strings.TrimPrefix(p, "socks5://")
	}
	return p
}

// refProxyArgs: git's arguments that send its HTTP through proxy (none for "").
func refProxyArgs(proxy string) []string {
	if proxy == "" {
		return nil
	}
	return []string{"-c", "http.proxy=" + proxy}
}

// refFetchRoutes: the routes a reference fetch is tried on, in order: direct, then the proxy
// when the machine names one.
func refFetchRoutes() []string {
	if p := refProxy(); p != "" {
		return []string{"", p}
	}
	return []string{""}
}

func refRouteName(proxy string) string {
	if proxy == "" {
		return "directly"
	}
	return "through " + proxy
}

// refUnreadableFmt: the reference files that could not be read, which stop ref_bind.
const refUnreadableFmt = "%s/%s %s: %d source file(s) could not be read from the reference clone, so the commit is not theorized: %s"

// refUnreadableRounds: how many times the files not read are asked for again.
var refUnreadableRounds = 3

// refUnreadable: the files of sha (the ones fileSurvey reads) that cannot be read from the
// reference clone after rounds attempts.
func refUnreadable(owner, name, sha string, files []string, rounds int) []string {
	var todo []string
	for _, f := range files {
		if !isBinaryFile(f) && !isDocOnlyFile(f) {
			todo = append(todo, f)
		}
	}
	for r := 0; r < rounds && len(todo) > 0; r++ {
		var left []string
		for _, f := range todo {
			if _, err := RefFileAt(owner, name, sha, f); err != nil {
				left = append(left, f)
			}
		}
		todo = left
	}
	return todo
}
