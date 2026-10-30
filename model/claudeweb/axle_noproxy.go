package claudeweb

// axle_noproxy.go — Axle is never reached through the environment proxy.
//
// Evidence, from the Strata runs on the local machines: single-file Axle calls
// made with the proxy stripped answer in 5-12 s ("axle-curl direct 1/2: got
// response"), and the Go client's "proxy" transport has no proxy at all and always
// answers in ~1 s — while the same kind of call through the environment proxy
// timed out again and again. The children's own verification scripts
// (/tmp/verify3.py, /tmp/axle.sh, ...) run curl via subprocess, inherit the
// proxy, and ran for 2h19m over a handful of files. Which script a child
// invents next cannot be predicted by name, so instead of recognising scripts,
// exempt the Axle host in the environment every child process inherits.
// NO_PROXY is honoured by curl, python requests/urllib and Go's
// ProxyFromEnvironment; every other host keeps using the proxy.
//
// EINSTEIN_AXLE_VIA_PROXY=1 disables this (for a network that can only reach
// Axle through its proxy).

import (
	"log"
	"os"
	"strings"
)

const axleHost = "axle.axiommath.ai"

func init() { ensureAxleNoProxy() }

func ensureAxleNoProxy() {
	if os.Getenv("EINSTEIN_AXLE_VIA_PROXY") == "1" {
		return
	}
	changed := false
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		cur := os.Getenv(k)
		if cur == "*" || strings.Contains(cur, axleHost) {
			continue
		}
		next := axleHost
		if strings.TrimSpace(cur) != "" {
			next = cur + "," + axleHost
		}
		os.Setenv(k, next)
		changed = true
	}
	if changed {
		log.Printf("claudeweb: %s now bypasses the env proxy (NO_PROXY); set EINSTEIN_AXLE_VIA_PROXY=1 to disable", axleHost)
	}
}
