package kernel

import (
	"sync"
	"time"
)

// The proxy in front of claude.ai answers 429 with a time when the account's quota comes
// back ("提问次数已达上限，请 2026-11-19 17:40:00 后重试"). Until then no conversation of this
// account is answered, so a unit started now would hold a mirror, a clone and a fork slot for
// hours and spend its tries on refusals. 11/19 07:16:39 the quota went at sixteen units side
// by side and every one of them slept 2h23m.
var (
	proxyQuotaMu    sync.Mutex
	proxyQuotaUntil time.Time
)

// SetProxyQuotaSpent records that the proxy's quota for this account is spent until t (the
// model layer calls it when the proxy says so).
func SetProxyQuotaSpent(t time.Time) {
	proxyQuotaMu.Lock()
	defer proxyQuotaMu.Unlock()
	if t.After(proxyQuotaUntil) {
		proxyQuotaUntil = t
	}
}

// ProxyQuotaSpent: how long the proxy's quota is still spent, or 0.
func ProxyQuotaSpent() time.Duration {
	proxyQuotaMu.Lock()
	defer proxyQuotaMu.Unlock()
	if d := time.Until(proxyQuotaUntil); d > 0 {
		return d
	}
	return 0
}

// The proxy can also refuse the account itself: "你当前的车暂时无法使用，请重新选择车辆" (403,
// 11/19 08:29:51, on the upload of unit 32's review). It names no time it comes back, so the
// account is unusable until a request of it is answered again. Meanwhile no unit is started
// beside the ones running; with none running, one may start, and its request is what finds
// out whether the account is back (a conversation the proxy ends that way is not an attempt).
var (
	proxyUnusableMu    sync.Mutex
	proxyUnusableSince time.Time
	proxyUnusableWhy   string
)

// SetProxyAccountUnusable records that the proxy refused the account itself (the model layer
// calls it with what the proxy said).
func SetProxyAccountUnusable(why string) {
	proxyUnusableMu.Lock()
	defer proxyUnusableMu.Unlock()
	if proxyUnusableSince.IsZero() {
		proxyUnusableSince = time.Now()
	}
	proxyUnusableWhy = why
}

// ProxyAccountAnswered records that a request of the account was answered: it is usable.
func ProxyAccountAnswered() {
	proxyUnusableMu.Lock()
	defer proxyUnusableMu.Unlock()
	proxyUnusableSince, proxyUnusableWhy = time.Time{}, ""
}

// ProxyAccountUnusable: since when the proxy has refused the account and what it said, or a
// zero time when its last word was an answer.
func ProxyAccountUnusable() (time.Time, string) {
	proxyUnusableMu.Lock()
	defer proxyUnusableMu.Unlock()
	return proxyUnusableSince, proxyUnusableWhy
}
