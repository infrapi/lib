package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"sync"
	"time"
)

// DefaultCacheTTL is what ClientOptions.CacheTTL defaults to when left
// zero: long enough to spare an active session repeated round trips to
// the authentication service, short enough that a revoked token or a
// changed group membership is not served stale for long.
const DefaultCacheTTL = time.Hour

// cacheEntry is one cached Verify result.
type cacheEntry struct {
	info      *UserInfo
	expiresAt time.Time
}

// cache is an in-memory, per-Client store of recent Verify results, keyed
// by a hash of the token rather than the token itself so a heap dump or a
// debug log of the cache's keys never reveals a usable credential.
//
// Nothing evicts an entry before it is both expired and looked up again,
// except the occasional sweep set triggers - acceptable because the
// number of distinct tokens active within ttl is bounded by the number of
// sessions actually using the service, not by request volume.
type cache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
	sets    int
}

func newCache(ttl time.Duration) *cache {
	return &cache{ttl: ttl}
}

func (c *cache) get(token string) (*UserInfo, bool) {
	key := cacheKey(token)

	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		ok = false
	}
	c.mu.Unlock()

	if !ok {
		return nil, false
	}
	return cloneUserInfo(entry.info), true
}

func (c *cache) set(token string, info *UserInfo) {
	key := cacheKey(token)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[key] = cacheEntry{info: cloneUserInfo(info), expiresAt: time.Now().Add(c.ttl)}

	// Bound memory held by tokens seen once and never again: sweep
	// expired entries occasionally rather than tracking each one's
	// deadline individually.
	c.sets++
	if c.sets%256 == 0 {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
	}
}

func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// cloneUserInfo deep-copies info so the cache's copy and a caller's copy
// never share the Attributes map or the Groups slice: neither side can
// mutate what the other reads.
func cloneUserInfo(info *UserInfo) *UserInfo {
	clone := *info
	clone.Attributes = maps.Clone(info.Attributes)
	clone.Groups = slices.Clone(info.Groups)
	return &clone
}
