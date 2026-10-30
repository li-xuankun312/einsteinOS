package claudeweb

// axle_path.go — how Axle is reached: directly, or through the environment proxy.
//
// What the logs show: on the Strata machines the environment proxy
// (socks5://127.0.0.1:1080) is needed to reach most external hosts, but for Axle
// it is a liability — the same 6-request script took 8m44s through it and 9.4s
// without it (~87 s vs ~1.6 s per request), and it is what turned a loop over 88
// files into two hours. On a network where Axle cannot be reached directly, though,
// the proxy is the only way in. So this is decided by looking, not hard-coded:
//
//	auto   (default) probe Axle directly at start-up. Reachable -> bypass the proxy
//	       for Axle only (NO_PROXY for every child process); unreachable -> leave the
//	       proxy alone.
//	direct force the bypass.   proxy force the proxy.
//
// EINSTEIN_AXLE_PROXY=auto|direct|proxy; EINSTEIN_AXLE_VIA_PROXY=1 still means proxy.
// Only the Axle host is ever touched; every other host keeps using the proxy.

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const axleHost = "axle.axiommath.ai"

// until a probe or a successful call says otherwise, try direct first: every
// successful Axle call in the logs went direct.
func init() { axleDirectPreferred.Store(true) }

var configureAxleOnce sync.Once

// configureAxleProxy runs once at boot (BuildSystemPrompt). It may take a few
// seconds if Axle is unreachable directly.
func configureAxleProxy() {
	configureAxleOnce.Do(func() {
		mode := axleProxyMode()
		switch mode {
		case "proxy":
			axleDirectPreferred.Store(false)
			log.Printf("claudeweb: Axle path: forced via the env proxy (EINSTEIN_AXLE_PROXY=proxy)")
			return
		case "direct":
			axleDirectPreferred.Store(true)
			setAxleNoProxy(true)
			log.Printf("claudeweb: Axle path: forced direct (EINSTEIN_AXLE_PROXY=direct); %s now bypasses the env proxy", axleHost)
			return
		}
		root := axleRoot()
		ok, took, derr := probeAxle(root, nil)
		if !ok { // one retry: a single blip must not decide the whole run
			time.Sleep(time.Second)
			ok, took, derr = probeAxle(root, nil)
		}
		if ok {
			axleDirectPreferred.Store(true)
			setAxleNoProxy(true)
			log.Printf("claudeweb: Axle path: direct works (%s); %s now bypasses the env proxy for child processes", took.Round(time.Millisecond), axleHost)
			return
		}
		pf := envProxyFunc()
		if pf == nil {
			log.Printf("claudeweb: Axle path: direct unreachable (%v) and no env proxy configured", derr)
			return
		}
		pok, ptook, perr := probeAxle(root, pf)
		axleDirectPreferred.Store(false)
		if pok {
			log.Printf("claudeweb: Axle path: direct unreachable (%v) but the env proxy works (%s); keeping the proxy for Axle", derr, ptook.Round(time.Millisecond))
		} else {
			log.Printf("claudeweb: Axle path: unreachable directly (%v) and via the env proxy (%v); keeping the proxy", derr, perr)
		}
	})
}

func axleProxyMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("EINSTEIN_AXLE_PROXY"))) {
	case "direct":
		return "direct"
	case "proxy":
		return "proxy"
	}
	if os.Getenv("EINSTEIN_AXLE_VIA_PROXY") == "1" {
		return "proxy"
	}
	return "auto"
}

// axleRoot is the scheme+host of axleURL, so the probe follows whatever the Go
// path posts to.
func axleRoot() string {
	u, err := url.Parse(axleURL)
	if err != nil || u.Host == "" {
		return "https://" + axleHost + "/"
	}
	return u.Scheme + "://" + u.Host + "/"
}

// probeAxle: any HTTP response at all (even 4xx) means the host is reachable —
// a completed TLS handshake is what the proxy was getting wrong.
func probeAxle(root string, proxy func(*http.Request) (*url.URL, error)) (ok bool, took time.Duration, err error) {
	const timeout = 5 * time.Second
	tr := &http.Transport{Proxy: proxy, TLSHandshakeTimeout: timeout, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: timeout}
	t0 := time.Now()
	resp, err := c.Get(root)
	took = time.Since(t0)
	if err != nil {
		return false, took, err
	}
	resp.Body.Close()
	return true, took, nil
}

// envProxyFunc returns the proxy named by the environment, or nil if there is
// none. It deliberately ignores NO_PROXY: that variable is what this file adds
// for Axle, and the "via proxy" attempt must really go through the proxy.
func envProxyFunc() func(*http.Request) (*url.URL, error) {
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return http.ProxyURL(u)
			}
		}
	}
	return nil
}

// setAxleNoProxy adds or removes the Axle host in NO_PROXY/no_proxy, which curl,
// python requests/urllib and Go's ProxyFromEnvironment honour — so scripts a
// child invents (verify3.py, axle.sh, ...) need no special recognition.
func setAxleNoProxy(on bool) {
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		cur := os.Getenv(k)
		if cur == "*" {
			continue
		}
		var parts []string
		for _, p := range strings.Split(cur, ",") {
			if p = strings.TrimSpace(p); p != "" && p != axleHost {
				parts = append(parts, p)
			}
		}
		if on {
			parts = append(parts, axleHost)
		}
		if len(parts) == 0 {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, strings.Join(parts, ","))
		}
	}
}
