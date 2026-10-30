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
auth and network errors do not establish exhaustion. Each provider has a required
timezone and local refresh cutoff (defaults: America/Los_Angeles, 20:00). Existing
explicit provider settings are retained. The home offers a signed
GMT hour plus :00/:30 offset, or an IANA region for DST and other offsets.
A group's editable
monthly/annual date combines with that cutoff to open a 48-hour real-traffic
recovery window, with increasing backoff. Month-end anchors clamp without drift;
elapsed hours include timezone/DST rules. There is no background probe worker.

`/` and `/ltsp/accounts` serve the account home, with client connection examples
derived from the current origin. Management and per-account statistics links
open separate tabs so an unsaved account form stays in place. Python examples are
collapsed initially. The OpenAI base URL supports Responses, Chat Completions and
legacy Text Completions, subject to the selected model's supported operations.
Downstream inference
requires a native Virtual Key; missing, dummy and retired keys are rejected.
The home manages the uniquely named `ltsp-lan-clients` key in the native encrypted
store, reveals it on request, and copies examples with its current value. Generated
rotation and operator-supplied replacement share native rotation's transaction,
grant revocation and cache reload. Replacements use `sk-bf-` plus URL-safe
characters, 32–256 characters overall. The deployment uses zero rotation grace.
There is no second key store or user/quota system. Management remains open to the
trusted LAN, so this prevents accidental base-URL-only calls rather than a client
that deliberately accesses the management API.

The home separates account groups, native Bifrost accounts (API key references),
and provider settings into tables. Each native account's optional group dropdown
only lists saved groups from the same provider; the API also rejects cross-provider
assignments. The native account table adds, edits and deletes keys through native
provider APIs. A blank key on edit retains its secret, model rules and weight;
the page never fetches the provider secret. Association changes persist immediately
through `/api/ltsp/assignments`, independently of policy drafts. A failed delete
keeps the account; failed association cleanup after an actual delete is reported
as partial completion. There is no extra credential store.
Group and provider tables have separately scoped save buttons with dirty red dots.
Partial policy updates preserve the other section and current key associations
under the manager lock. Native key operations preserve both sections' local drafts.
Recovery details use a floating hover/focus panel; adding a group is below the list.
Balance placeholders belong to individual groups. No balance is
invented from usage costs. A provider's probe model is optional. When absent, or
when an explicit model-not-supported verdict invalidates it, the funding button
tries eligible catalog text models in random order, at most once each, until one
succeeds. That model is remembered in the same provider configuration. Credit,
authentication, rate-limit, network and unrelated parameter errors stop the search.
The whole button operation is bounded to 45 seconds and one reservation per group.
Unsaved policy changes must be saved before pressing the probe button.
Tables reconcile existing rows and choices by stable group/key/provider IDs.
Unchanged fields are not written; ordinary edits, saving and probe results preserve
DOM identity, focus, selection, expansion and scroll. Only new/deleted data items
create/remove corresponding nodes. Account groups use compact columns at wider
widths and labeled wrapping fields in split panes and narrow views, retaining the
same rows and inputs without horizontal scrolling. Native selects and option menus
share explicit light/dark foreground/background colors with form controls.
Each candidate is an ordinary logged inference through the gateway's native HTTP
admission pipeline using the current shared native Virtual Key, pinned to one
enabled, model-eligible key without native retry or fallback. A random, single-use,
in-memory marker admits that one attempt; it is
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
encrypted key registration persistence, statistics and content exclusion. It also
covers provider cutoff defaults, same-provider grouping, per-group balances, catalog
discovery and replacement of an unsupported preferred model across process restart,
native key CRUD with unchanged secret/model/weight fields, immediate assignment,
independent partial saves and fixed GMT offsets.
Credit exhaustion tests use this fixture because a live harness cannot safely
create a real billing exhaustion condition. Live harness cases cover the
Responses wire surfaces; the operator chooses whether to spend real credits.

Request-path cost: when disabled, extension hooks are behind a nil check.
Enabled account checks retain only operator-configured groups and transient
account reservations. The unmodified hot path creates no per-stream payload
state. `BenchmarkCacheBody` measures unrelated/explicit/default payloads and
`BenchmarkAccountAttempt` measures healthy begin/end checks; report results with
the deployment's build evidence rather than claiming unchanged latency.
`BenchmarkRetryWindow` measures the held-account calendar calculation. Parsed
timezone data is retained once per configured provider; discovery retains only a
bounded-by-catalog candidate list for the duration of a manual button operation.
