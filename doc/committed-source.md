# Committed rundown source

OpenMOS can publish independently retained rundowns and a catalogue to an application over
authenticated loopback HTTP. This is an opt-in file-storage mode. It carries neutral source
values; the receiving application owns media selection, graphics mappings, external API
identity and destination effects. A MOS retention ACK, an HTTP source receipt and an applied destination effect are
three separate results.

The implementation has synthetic local tests. It does not add a MOS profile advertisement,
live newsroom proof, deployment qualification or a guarantee of physical rendering.

## Provisioning and operation

Keep `STATE_DIR` pointed at the existing protocol state and select an unused
`SOURCE_STATE_DIR` for the committed checkpoint. The existing MOS identity, peer identity
and selected transport must also be configured and enabled.

```text
STORAGE_BACKEND=file
STATE_DIR=/private/native-state
SOURCE_STATE_DIR=/private/source-state
SOURCE_ENABLED=true
SOURCE_ID=synthetic-source
SOURCE_RUNDOWN_ID=synthetic-rundown
SOURCE_TRANSPORT=tcp
SOURCE_URL=http://127.0.0.1:19090/v1/openmos-snapshots
```

`SOURCE_TRANSPORT` is `tcp`, `ws-server` or `ws-client`. Set `SOURCE_TOKEN` privately in the
runtime environment; it is sent as a Bearer credential and excluded from generated YAML
and retained checkpoints. HTTP redirects, remote addresses and alternate routes are
rejected. The other source options can also be set in the YAML `source` block.

`SOURCE_STATE_DIR` is optional (`source.statedir` in YAML). When absent, both initialization
and normal startup use `STATE_DIR` for the checkpoint, preserving the existing behavior for
fresh isolated installations. With an override, `STATE_DIR` still holds native sender
message-ID marks, transport receipts, discovery state and the legacy file store. Legacy
file mode ignores the override. Keep that native directory and the same MOS identity through
source startup, restart and rollback so sender numbering continues from its retained mark.
The directory setting does not copy or migrate counters or legacy rundown content.

Run `openmos --initialize-source-state` once to provision a new directory, then start
`openmos` normally. Provisioning refuses an existing committed checkpoint and any legacy
`runningorders.json`; it never migrates, deletes or resets data. Normal startup refuses a
missing or unreadable checkpoint, a failed integrity check, or a changed source, rundown,
MOS peer, transport or destination binding. Starting legacy file mode against a committed
source directory also fails. Retain the checkpoint and its original configuration together.

One process owns the directory through a native file lock. A second opener is rejected;
normal exit or process termination releases the lock. The filesystem must support checked
file synchronization, atomic replacement and directory synchronization. An uncertain write
stops further publication and successful retention acknowledgements until a validated restart.

### Retaining multiple rundowns

Keep the existing primary source binding and checkpoint. Add a separate catalogue directory
and explicitly selected additional rundown bindings; the complete retained set is limited to
100 distinct IDs, including the primary. For example:

```text
SOURCE_CATALOGUE_STATE_DIR=/private/catalogue-state
SOURCE_ADDITIONAL_RUNDOWNS=[{"rundownId":"synthetic-second","stateDir":"/private/second-source-state"}]
```

The YAML equivalents are `source.cataloguestatedir` and `source.additional`, whose entries
use `rundownid` and `statedir`. The environment array is strict JSON: unknown fields, `null`
and trailing data are rejected. An explicit `[]` clears additional entries supplied by YAML.
State directories must be distinct from the primary and native protocol directories.

Provision each additional directory using the existing `--initialize-source-state` command,
temporarily selecting that exact `SOURCE_RUNDOWN_ID` and `SOURCE_STATE_DIR` and setting
`SOURCE_ADDITIONAL_RUNDOWNS=[]`. Then restore the primary configuration and run
`openmos --initialize-source-catalogue` once. This command creates only the separate catalogue;
it refuses existing catalogue or rundown state. Normal startup opens every configured store
before starting any transport or publisher and refuses a missing additional store.

Each rundown keeps the existing version 1 checkpoint format, revision, original receipts,
raw content and cue allocator. Catalogue state lives in `source-catalogue.json`, with its own
lock, integrity check, revision and receipts. Extra provisioning leaves the primary checkpoint
and native sender counters untouched. Removing the catalogue and additional configuration
restores the single-rundown mode; retain all files and the original binding for rollback.
The receiver must also support the selected mode. Startup always requires fresh authority.

All configured rundowns continue receiving and publishing edits regardless of which show the
application selects. Identical story, item or local cue IDs in different rundowns remain
separate. Replay conflicts are checked across the retained set because a peer's message-ID
sequence spans shows. There is still one native MOS identity and transport counter stream.
The application owns show selection, association retention and destination effects; OpenMOS
adds no selection endpoint or inbound application listener.

## Retention, completeness and replay

Each accepted mutation is applied to detached repositories. Repository state, the latest raw
roster and story bodies, source coverage, the monotonic revision, exact pending HTTP body,
and applicable original MOS response are replaced in one versioned checkpoint. The file and
directory are synchronized before a successful MOS reply is released. Failed application
work is discarded; a negative receipt and incomplete state retain the prior repository data.
Direct entity writes cannot bypass this transaction in committed-source mode.

A complete `roCreate`, `roList` or `roReplace` establishes the active roster and its order.
Every current story then needs a fresh, explicitly present `storyBody`. Full replacements
remove absent members; an explicitly empty body or roster is authoritative. `roReadyToAir`
remains metadata. `roDelete` deactivates the rundown. Startup, reconnect, lost source health,
malformed frames or rejected identities on an owned source connection, unsupported source
mutations and failed recovery invalidate coverage. They never turn an
old file or an exhausted discovery walk into proof of completeness.

For a committed `ws-client` source, the passive lane checks control Ping/Pong at the existing
heartbeat interval. A matching Pong renews only a validated, unexpired current session. It
does not register a connection, change the source revision or restore roster/body freshness.
A failed Ping ends the connection; normal reconnect and fresh-coverage rules still apply.
When the passive lane reconnects, an available request lane asks for the selected rundown
through the existing recovery walk. A request lane becomes available only after Profile 0;
if its handshake is pending, its normal discovery starts recovery after the handshake.
Neither reconnection nor replayed acknowledgements make retained story bodies fresh.

With catalogue mode enabled, a `ws-client` startup or passive reconnect requests a new full
enumeration on the handshaken request lane, followed by one roster request per configured,
advertised rundown.
Catalogue and roster requests share the same serialized discovery walk. A new passive session
that validates after the first catalogue reply queues another enumeration. Lost responses
use the existing bounded discovery timeout, checked when validated traffic arrives; Profile 0
traffic can advance catalogue recovery too. A quiet connection with no MOS input leaves that
walk waiting, while normal source liveness checks continue to fence publication.

A catalogue or roster response must match the actual request type, rundown, sending scope,
connection and MOS 4 request ID. The request is registered before writing, and its slot stays
occupied while the matching response is retained. Unsolicited, timed-out and replayed responses
cannot certify membership or coverage, or resolve a newer request. Native TCP correlates by
its sole outstanding connection because it has no request ID; after an ambiguous timeout or write failure, recovery waits
for a replacement connection. It does not send a second ambiguous request on that connection.
Validated input after a source liveness gap invalidates coverage before renewing the session,
even when the publisher has not yet swept the expired interval.

The source retains current raw XML even when it exceeds the receiver's publication limits.
It then publishes `complete:false` with `stories:[]` and records the reason in the checkpoint's
`source.state.problem`. This suspends preparation while the receiver preserves its prior
associations. `complete:true` with an empty story list is an authoritative empty roster.
Structural ambiguity and unsupported wire shapes may be rejected with a NACK; a successful
retention ACK always means the accepted content is durably owned by OpenMOS.

Incoming messages with an identifier retain their input hash and original response without
eviction. Identical retries replay the original transport response and cannot refresh body
coverage. Peer requests and responses to device requests have separate identifier sequences;
the same numeric ID may occur in both directions. New MOS 4 device requests use the transport's
own sequence; native MOS 2.x requests omit the ID. Responses echo a supplied request ID.
Changed content under the same identifier within either direction is rejected. MOS 2.x inputs
without a message identifier get atomic replacement but cannot claim retry deduplication.
Receipts accumulate while repository and raw source data keep only their latest state. The
checkpoint therefore has linear storage and rewrite cost in receipt count. An indexed receipt
store is deferred until measured volume justifies it; there is no automatic retirement,
counter reset, migration, broker or alternate source generation.

The synthetic retention check produced a checkpoint of about 745 KiB for 4098 compact
receipts and one empty story. Real response sizes and retained bodies determine the cost;
each commit rewrites the whole checkpoint. Each retained rundown has that same storage cost;
cross-rundown replay checks scan their retained receipts. Sustained-volume throughput remains
unqualified.

## Mixed order and local cue continuity

Body extraction follows XML document order across direct items, paragraph items, inline
text, paragraph-spanning commands and production instructions. Unsupported structure or a
command crossing an item boundary cannot certify order. Item occurrence identity uses the
source item ID within its source/rundown/story scope; it never uses an object ID or label.

Every anonymous cue receives its own persisted local ID, including identical, repeated and
back-to-back cues. Equal editorial content is never deduplicated. The existing monotonic
allocator skips explicit item IDs and commits allocations with content, revision and reply;
transport receipt replay does not allocate again.

Unchanged regions retain their local allocations. Regions lie between consecutive explicit
item IDs, or a story boundary, while the complete item-ID order remains unchanged. Within
one region, unique unchanged type/verb/target selectors also permit payload edits in their
existing slots. Fields, parameters and raw text are payload, never keys for matching a moved
cue. These are local continuity policies, not proof of upstream cue lineage.

An edit to a region with repeated selectors, reordered slots, changed selectors or changed
cue count replaces all anonymous allocations in that region with fresh IDs. Without stable
item anchors the region is the whole story; changed item-ID order likewise replaces that
story's anonymous allocations. Explicit items and other stories retain their identities.
A fresh authoritative body also replaces unresolved allocations in older checkpoints,
preserving their allocator high-water mark. None of these cases requires a state reset or
suspends completeness solely because historical cue lineage is unknown. Fresh roster/body
coverage and the other publication checks still apply.

Fresh local IDs explicitly replace prior occurrences; they do not authorize repurposing a
destination resource. The receiving application owns safe retirement of previous resources,
including retaining ownership and visibly deferring removal while a resource is in use.

## Neutral HTTP version 1

`POST /v1/openmos-snapshots` carries:

```json
{"version":1,"sourceId":"synthetic-source","rundownId":"synthetic-rundown","revision":1,"active":true,"complete":true,"stories":[{"id":"story","occurrences":[{"id":"item","kind":"mos_item","mosId":"media.example","objId":"object","itemEdDur":"00:00:01.25","media":[{"role":"objPath","url":"https://media.invalid/clip.mp4"}]}]}]}
```

`mos_item` may also carry `objType`, `label`, `abstract`, opaque `metadata` XML strings and
the raw strings `objDur` and `objTB`. Media entries carry `role`, `url` and optional
`techDescription`; roles retain wire names such as `objPath`, `objProxyPath` and
`objMetadataPath`. A `cue` carries `cueType` and optional `target`, `raw`, `verb`, `fields`,
`params` and opaque `metadata`. Unknown semantic types are carried without interpretation.
Optional absence is distinct from explicit empty strings, empty arrays or empty positional
fields. Durations and metadata payloads are never interpreted for destination use.

In catalogue mode, each story can also carry optional `page` and `slug` strings. These come
from present standard `storyNum` and `storySlug` properties in the retained roster, overridden
only by a present property in the current story body message. Explicit empty values override;
absence does not. Values retain whitespace and allow 512 Unicode code points. No page number
is invented, and no `segment` is inferred from text or opaque external metadata. Display fields
are projected from already retained raw XML, without extending the rundown checkpoint state.

The publication limits are 100 stories, 200 total occurrences and 64 KiB of complete UTF-8
JSON. IDs, types, labels, raw timing, verbs, parameter keys, media roles and technical
descriptions allow 512 Unicode code points; abstracts, URLs, cue fields and parameter values
allow 2048. Raw cues and each metadata block allow 16384. Media, metadata, field arrays and
parameter maps allow 32 entries. Story IDs and mixed occurrence IDs must be unique in their
respective scopes. Revisions are positive and no greater than 9007199254740991. No field or
list is truncated to fit.

Each rundown's publisher sends its exact latest committed body. A lost reply retries the
same revision and bytes; a newer committed revision supersedes pending older work. A late
HTTP receipt cannot mark a newer revision accepted. Only a matching `acceptedRevision`, `duplicate` and
`destinationApplied:false` response counts as source acceptance. Current retained authority
renews by identical duplicate every ten seconds for the receiver's thirty-second lease and
revalidates a restarted receiver. A missing source connection or expired inbound liveness
produces an incomplete revision. Receiver unavailability cannot block MOS retention; HTTP
409 halts publication without bumping or resetting the source counter.

In catalogue mode, snapshot receipts must additionally echo the exact `sourceId` and
`rundownId`. Acceptance and duplicate renewal apply only to that pair; receiving a receipt
for one rundown never marks another accepted. Single-rundown mode retains compatibility with
the original receipt shape. A conflict for one rundown stops that rundown's publisher across
reconnect and restart while the other configured rundowns continue to retain and publish.
Recovery never resets or changes the halted rundown's retained counter, content or receipts.

### Catalogue version 1

`POST /v1/openmos-catalogue` uses the same numeric loopback origin and producer Bearer
credential as the snapshot endpoint:

```json
{"version":1,"sourceId":"synthetic-source","revision":1,"complete":true,"rundowns":[{"id":"synthetic-rundown","active":true,"label":"Synthetic show","scheduledStart":"2030-01-02T10:00:00"}]}
```

The catalogue covers the explicitly configured retained set. A fresh full `roListAll`
establishes which of those IDs are MOS-active; out-of-set IDs are excluded from publication
and roster discovery. Retained `roCreate` and `roDelete` update that membership. Roster and
metadata replacements update an existing entry's optional display values. `active` describes
MOS membership separately from snapshot completeness. The receiver requires both current
active membership and a fresh active complete snapshot before permitting selection.

Optional `label` carries the present `roSlug`; `scheduledStart` carries the present raw
`roEdStart`. Absence and explicit empty remain distinct. No nulls, inferred labels, date
conversion or default schedules are emitted. The body permits at most 100 unique rundown IDs
and 64 KiB, with 512 Unicode code points per scalar string. It never truncates a catalogue to
fit. A malformed or over-limit retained-set enumeration leaves it incomplete.

Startup, connection replacement, uncertain input or lost liveness publishes `complete:false`
with `rundowns:[]`; only a fresh full enumeration restores coverage. An authoritative empty
enumeration publishes `complete:true` with that empty list. An identical input receipt replay
cannot restore coverage. A fresh identical catalogue retains its revision, while changed
canonical content advances the independent catalogue counter.

The matching durable receipt is
`{"sourceId":"synthetic-source","acceptedRevision":1,"duplicate":false,"destinationApplied":false}`.
Duplicate publication every ten seconds renews only the receiver's catalogue lease. Catalogue
receipts never renew a rundown lease or imply destination application. A late receipt cannot
accept a newer catalogue body; HTTP 409 halts the catalogue stream without resetting counters.

Focused repository, service and actual TCP/WebSocket ingress tests cover these boundaries.
The normal Go build, vet, repeated test and race checks remain required. Source freshness
against a particular newsroom still requires independent evidence of its full-roster and
fresh-body delivery sequence.
