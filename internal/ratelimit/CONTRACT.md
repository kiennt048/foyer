# internal/ratelimit — CONTRACT

Token buckets keyed by string, for the sign-in forms. The package comment has the
survey of what Keycloak, authentik and Django-axes do and why this lands on
authentik's shape.

## Invariants

1. **A refusal spends nothing and says how long to wait.** `Allow` returns the time
   until one token is back; `internal/api` rounds it up to whole seconds and never
   answers zero.
2. **It refills.** A bucket is not a lockout: holding a key shut costs the holder a
   steady stream of requests. `TestItRefills` proves the recovery.
3. **The map is bounded** at `DefaultMaxKeys`. Fully refilled keys are swept; past
   the cap a limiter from `New` refuses a *new* key and evicts none, because eviction
   would let a flood of fresh keys reset any bucket. **A limiter from `NewEvicting`
   forgets its least recently seen key and admits the newcomer**, because where
   keys are cheap to name, *refuse the newcomer* is a lockout of everyone who
   arrives after the flood. The caller that chooses it owes a reason its keys are **not** cheap — the portal's are TCP
   sources, and a verified MAC on one — and a test; `TestAnEvictingLimiterNeverRefusesANewcomer`
   is this package's.
4. **`ClientKey` is the socket peer, never a header**, and an IPv6 peer is keyed on
   its /64 so one subscriber cannot rotate through addresses.
5. **In-process.** Two replicas get twice the budget. v1 runs one.
6. **The bound is in keys, so a key must be bounded in bytes by its caller.** The
   limiter stores the key string as given. A key built from caller-typed text (a
   username) must be digested first, or `DefaultMaxKeys` bounds the count while
   each entry is as large as a request body — `internal/api`'s `accountKey` does
   this.
7. **`SetPolicy` reshapes without refunding** (the console's throttle settings).
   Every held bucket is refilled to now under the old policy and capped at the new
   burst; nobody gains tokens by a change, and a lowered burst binds on the
   next attempt. Replacing a limiter instead would forget every bucket.
8. **Two shapes, one package.** `Limiter` is a token bucket, for attempts;
   `Window` is a fixed window — `limit` per `per`, then nothing until the
   window is over — for sends that cost money, where a bucket would let twice the
   limit through in the first period. `Window` is bounded by the same rule as
   `Limiter` (invariant 3: past `DefaultMaxKeys` a new key is refused and none is
   evicted).
   The limit and period are per call, so a setting changed in the console binds on
   the next event.
