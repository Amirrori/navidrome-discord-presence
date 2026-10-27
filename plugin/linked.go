package main

import (
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// The plugin only works for users who linked a Discord account: for everyone
// else it sends nothing and looks nothing up. The answer is cached so the
// bridge is asked at most every few minutes per user.
const (
	linkedCacheTTL   int64 = 10 * 60 // linked users are re-checked every 10 minutes
	unlinkedCacheTTL int64 = 2 * 60  // a fresh link starts working within 2 minutes

	linkedValue   = "1"
	unlinkedValue = "0"
)

// isLinked reports whether the user has linked a Discord account. When the
// bridge can't be asked, it answers false, so nothing is sent or looked up.
func isLinked(cfg bridgeConfig, username string) bool {
	if cached, exists, err := host.CacheGetString(linkedCacheKey(username)); err == nil && exists {
		return cached == linkedValue
	}

	linked, err := queryLinked(cfg, username)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Asking the bridge about user %s failed: %v", username, err))
		return false
	}
	rememberLinked(username, linked)
	return linked
}

func rememberLinked(username string, linked bool) {
	value, ttl := unlinkedValue, unlinkedCacheTTL
	if linked {
		value, ttl = linkedValue, linkedCacheTTL
	}
	_ = host.CacheSetString(linkedCacheKey(username), value, ttl)
}

func linkedCacheKey(username string) string {
	return "linked." + hashKey(username)
}
