# LTSP LAN gateway fork

This branch adds the LAN gateway behavior used by the LTSP controllers to
upstream `transports/v2.3.2` (`a05ea20e68bf116a35e9f05b1a3d749624b3935e`).
The checkout is `/ws/AiWorks/bifrost`; the fork is `longrun-ai/bifrost`.

`LTSP_GATEWAY_STATE` explicitly enables the extension. Its JSON contains account
groups and credit recovery state, never API key values. Bifrost's own encrypted
configuration store owns keys. Without that environment variable, native auth,
setup, routing and cache defaults remain in charge. An enabled gateway exposes
management without login for the trusted LAN, preserving Origin checks. The
operator binds it to the controller's LAN IP and supplies `LTSP_GATEWAY_PROXY`.

The native provider picker owns weights, model eligibility and session affinity.
Missing session IDs receive source-IP affinity. Account policy runs at the real
provider attempt boundary, so a held sibling key cannot bypass a shared balance.
Only explicit upstream credit exhaustion pauses an account; generic quota, rate,
auth and network errors do not establish exhaustion. The editable monthly or
annual refresh date permits real-traffic recovery on the date and adjacent days,
with increasing backoff. There is no background probe worker.

`/` and `/ltsp/accounts` serve the account home, with client connection examples
derived from the current origin. Management and per-account statistics links
open separate tabs so an unsaved account form stays in place. The SDK's
`dummy-api-key` is a non-secret placeholder while inference auth is disabled;
actual provider credentials remain in native key management. Native Virtual
Keys provide a separate opt-in path for real client authentication and rotation.

The account home groups native keys and configures dates and probe models. Its
funding button immediately sends one logged inference through the gateway's
native HTTP admission pipeline, pinned to one enabled key without retry or
fallback. A random, single-use, in-memory marker admits that one probe; it is
removed before request header capture and freed at consumption or completion.
There is at most one outstanding probe per account. State writes use fsync and
atomic rename. A successful probe clears the hold; another failure does not.

Supported OpenAI Responses models receive `prompt_cache_options.ttl = "12h"`
only when a downstream policy is absent. Explicit request and block policies
survive conversion. Official 30m/12h usage fields are preserved, and the native
cost calculator separates reads, ordinary inputs and writes at 1.25x/2.5x.
The native dashboard remains the usage owner; no balance endpoint is assumed.

Build using `make setup-workspace` and `make build LOCAL=1 VERSION=2.3.2-ltsp`.
Frontend source lives at `ui/public/ltsp/accounts.html` and travels through the
normal Vite copy/embed pipeline. Controller packaging, private storage locations,
systemd and A/B adoption belong to the LTSP deployment repository.

Validation commands:

```sh
(cd transports && go test -race ./bifrost-http/ltsp)
(cd transports && go test ./bifrost-http/ltsp -run '^$' -bench . -benchmem)
(cd framework && go test ./modelcatalog/datasheet -run TestLTSPCacheWriteCostsUseLifetimeSplit)
python3 tests/ltspgateway.py --binary "$PWD/tmp/bifrost-http" --evidence /tmp/ltsp-gateway-http
```

The local HTTP test starts the actual binary with fake keys and an isolated
proxy/provider fixture. It covers unary/streaming, both providers, request cache
defaults and explicit policies, affinity, exhaustion/fallback, immediate probes,
encrypted key registration persistence, statistics and content exclusion.
Credit exhaustion tests use this fixture because a live harness cannot safely
create a real billing exhaustion condition. Live harness cases cover the
Responses wire surfaces; the operator chooses whether to spend real credits.

Request-path cost: when disabled, extension hooks are behind a nil check.
Enabled account checks retain only operator-configured groups and transient
account reservations. The unmodified hot path creates no per-stream payload
state. `BenchmarkCacheBody` measures unrelated/explicit/default payloads and
`BenchmarkAccountAttempt` measures healthy begin/end checks; report results with
the deployment's build evidence rather than claiming unchanged latency.
