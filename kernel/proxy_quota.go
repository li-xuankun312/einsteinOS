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
