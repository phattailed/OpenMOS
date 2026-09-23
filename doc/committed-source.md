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

### Discovering and retaining multiple rundowns

Keep existing primary and additional source bindings and checkpoints. Set a separate
catalogue directory as the root for discovered members. A fresh correlated full `roListAll`
from the configured MOS/NCS identity enrolls every advertised rundown, including IDs absent
from configuration. No show selection, fixture allowlist or per-show restart is involved.
For example, an existing primary and explicitly provisioned second source can continue using:

```text
SOURCE_CATALOGUE_STATE_DIR=/private/catalogue-state
SOURCE_ADDITIONAL_RUNDOWNS=[{"rundownId":"synthetic-second","stateDir":"/private/second-source-state"}]
```

The YAML equivalents are `source.cataloguestatedir` and `source.additional`, whose entries
use `rundownid` and `statedir`. The environment array is strict JSON: unknown fields, `null`
and trailing data are rejected. An explicit `[]` clears additional entries supplied by YAML.
State directories must be distinct from the primary and native protocol directories. Explicit
entries preserve their current paths; discovery does not move or replace those checkpoints.

Provision each additional directory using the existing `--initialize-source-state` command,
temporarily selecting that exact `SOURCE_RUNDOWN_ID` and `SOURCE_STATE_DIR` and setting
`SOURCE_ADDITIONAL_RUNDOWNS=[]`. Then restore the primary configuration and run
`openmos --initialize-source-catalogue` once. This command creates only the separate catalogue;
it refuses existing catalogue or rundown state. A new catalogue-only source can instead omit
`SOURCE_RUNDOWN_ID` and set `SOURCE_ADDITIONAL_RUNDOWNS=[]`, initialize the catalogue with that
same command, and discover its first members at runtime. `SOURCE_STATE_DIR` is unused in that
case. Normal startup opens every configured and previously retained store before starting any
transport or publisher and refuses missing retained state.

Each rundown keeps the existing version 1 checkpoint format, revision, original receipts,
raw content and cue allocator. Catalogue state lives in `source-catalogue.json`, with its own
lock, integrity check, revision and receipts. A separate integrity-checked `source-members.json`
records retained IDs independently of active catalogue membership. Its optional `unenrolled`
list marks receipt-only stores that have never received authoritative enrollment. Each checkpoint
lives at `<SOURCE_CATALOGUE_STATE_DIR>/rundowns/<sha256>/source-checkpoint.json`, where `sha256`
is the lowercase SHA-256 hex digest of the exact UTF-8 rundown ID. IDs remain opaque and
unchanged inside checkpoints and publication; path separators never become directory names.
Occupied paths with a different binding, symlinked member directories and missing inventory
beside member state fail closed.

Enrollment writes and synchronizes an initial checkpoint in a fixed staging directory, installs
it under the digest key, then durably records its ID before accepting input. An interrupted
initial enrollment can resume after restart. Existing accepted member state is never
reinitialized or replaced. Validated unknown input with a message ID retains its original NACK
in an ordinary receipt-only checkpoint before sending it, without accepting the input's content.
That store remains outside routing, discovery and publication until full enumeration admits it.
Enrollment reuses the same checkpoint and receipts; restart preserves the unenrolled marker.
If storage or capacity prevents durable refusal, the source returns an error without sending
an unretained ACK. Inputs without a message ID retain the existing native retry limits below.
Member removal from the active catalogue does not retire its store, raw content, counters or
original receipts. The retained-store ceiling includes the primary, all explicit additions,
all enrolled members including inactive members, and all receipt-only stores: 100 for version 1
and 512 for version 2. Full enumeration is validated against publication and discovery capacity
before enrollment; exhausted capacity produces an error and withholds completeness without
eviction or a truncated authoritative list. A storage failure may leave safely retained
partial enrollment, but cannot certify the full enumeration. Recovery requires fresh authority.

The receiver must support the selected publication version. Startup always requires fresh
authority and keeps previously retained catalogue rows as incomplete observations.

All enrolled rundowns continue receiving and publishing edits regardless of which show the
application selects. Identical story, item or local cue IDs in different rundowns remain
separate. Replay conflicts are checked across the retained set because a peer's message-ID
sequence spans shows. There is still one native MOS identity and transport counter stream.
The application owns show selection, association retention and destination effects; OpenMOS
adds no selection endpoint or inbound application listener.

### Compatible configuration rollback

The retained version 1 stores use the existing strict checkpoint schemas. A compatible older
producer can reopen them when every retained member is explicitly configured.
Stop the current producer first, retain its complete current state, and derive the old
configuration from the original explicit bindings plus **all** IDs in the current
`source-members.json`, including inactive and receipt-only IDs. For each retained ID, use its
exact string as `rundownId` and the digest directory above as `stateDir`; verify that the
checkpoint binding matches. Deduplicate an ID only when it names the same current store. Keep the original primary
if present; otherwise choose one retained member as `SOURCE_RUNDOWN_ID`/`SOURCE_STATE_DIR` and
put every remaining member in `SOURCE_ADDITIONAL_RUNDOWNS`. Preserve `SOURCE_ID`, MOS/NCS identity,
transport, publication destination, credential, native `STATE_DIR` and catalogue directory.
An older producer requiring a primary cannot run an empty catalogue-only source.

Reopen the current checkpoints, never pre-cutover copies or `.pre-v2` backups. Removing the
catalogue or additional bindings, disabling committed-source mode, or restoring an earlier
checkpoint would stop retaining members or lose accepted receipts and is not this rollback
route. The independent inventory must stay alongside the catalogue for a later upgrade; the
older producer leaves it alone. An older binary does not enforce the `unenrolled` marker:
explicitly configured receipt-only stores follow its existing configured-member admission
rules, while their original NACKs still replay unchanged. Upgrading again restores the marker's
enrollment gate until fresh full enumeration arrives. Further unknown members require explicit
provisioning while running the older version. Its ordinary startup still requires fresh
roster/body authority.
This route applies only to a version known to understand all current checkpoint fields and
the selected publication format; a version 1-only producer cannot reopen version 2 state.

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
enumeration on the handshaken request lane, followed by one roster request per advertised
rundown. Incidental traffic for an unknown ID invalidates catalogue authority and requests a
fresh enumeration through the same walk; it cannot enroll a member. This also works on an
already healthy connection. Profile 0 traffic can request refresh on an available TCP or
WebSocket lane. A full enumeration expires after the configured source timeout even when
connection heartbeats remain healthy; its expiration does not invalidate independently fresh
rundown snapshots.
Catalogue and roster requests share the same serialized discovery walk. A new passive session
that validates after the first catalogue reply queues another enumeration. Lost responses
use the existing bounded discovery timeout, checked when validated traffic arrives; Profile 0
traffic can advance catalogue recovery too. A quiet connection with no MOS input leaves that
walk waiting, while normal source liveness checks continue to fence publication.
An enumeration refresh preserves queued, still-advertised rosters ahead of another pass,
removes absent IDs and appends other advertised IDs once. A slow walk therefore keeps making
progress even when catalogue freshness expires before all its rosters have arrived.

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

The snapshot can also carry optional top-level `metadata`: an ordered array of complete,
opaque `mosExternalMetadata` XML strings for the rundown. A full `roCreate`, `roReplace` or
`roList` establishes this list; `roMetadataReplace` replaces it without changing stories.
An authoritative message that omits the blocks establishes `metadata:[]`, as does rundown
deletion. An empty `mosPayload` remains a block. No scope, schema or payload is interpreted.
The array uses the occurrence metadata bounds: at most 32 strings, each valid UTF-8 and at
most 16384 Unicode code points, within the 128 KiB complete JSON limit. Content
outside publication limits stays in the MOS repository and source checkpoint; publication
becomes incomplete and omits metadata rather than truncating it or claiming an empty list.

Metadata and its pending projection survive checkpoint reopening and receipt replay.
Older checkpoints remain readable; omitted metadata means not yet established until a fresh
authoritative message arrives. A compatible receiver must be deployed before this producer:
earlier receivers reject the additional top-level property. Earlier producers with a strict
source-state decoder cannot read a checkpoint after its new metadata field has been written;
an operational downgrade requires a separately validated compatible reader, not a state reset.

In catalogue mode, each story can also carry optional `page` and `slug` strings. These come
from present standard `storyNum` and `storySlug` properties in the retained roster, overridden
only by a present property in the current story body message. Explicit empty values override;
absence does not. Values retain whitespace and allow 512 Unicode code points. No page number
is invented, and no `segment` is inferred from text or opaque external metadata. Display fields
are projected from already retained raw XML, without extending the rundown checkpoint state.

The publication limits are 100 stories, 200 total occurrences and 128 KiB of complete UTF-8
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

The catalogue covers every member of a fresh full correlated `roListAll` from the configured
source identity. It enrolls unknown opaque IDs before granting complete membership authority.
A validated `roDelete` removes that member from the catalogue without deleting its store.
A `roCreate` for a member absent from the latest catalogue requires a new enumeration; it
does not establish membership by itself. Roster and
metadata replacements update an existing entry's optional display values. `active` describes
MOS membership separately from snapshot completeness. The receiver requires both current
active membership and a fresh active complete snapshot before permitting selection. A newly
enrolled or reappearing member starts with incomplete snapshot coverage; full membership can
be known while its roster or bodies are still unavailable.

Optional `label` carries the present `roSlug`; `scheduledStart` carries the present raw
`roEdStart`. Absence and explicit empty remain distinct. No nulls, inferred labels, date
conversion or default schedules are emitted. The body permits at most 100 unique rundown IDs
and 64 KiB, with 512 Unicode code points per scalar string. It never truncates a catalogue to
fit. A malformed or over-limit enumeration leaves it incomplete.

Startup, connection replacement, uncertain input or lost liveness publishes `complete:false`
with the previous rows retained as observations, without removal or snapshot-readiness
authority. Only a fresh full enumeration restores coverage. An authoritative empty enumeration
publishes `complete:true` with `rundowns:[]`; complete absence invalidates the absent member's
snapshot coverage while retaining its store and history. An identical input receipt replay
cannot restore coverage or extend the enumeration lifetime. A fresh identical catalogue retains
its revision, while changed canonical content advances the independent catalogue counter.

The matching durable receipt is
`{"sourceId":"synthetic-source","acceptedRevision":1,"duplicate":false,"destinationApplied":false}`.
Duplicate publication every ten seconds renews only the receiver's catalogue lease. Catalogue
receipts never renew a rundown lease or imply destination application. A late receipt cannot
accept a newer catalogue body; HTTP 409 halts the catalogue stream without resetting counters.

Focused repository, service and actual TCP/WebSocket ingress tests cover these boundaries.
The normal Go build, vet, repeated test and race checks remain required. Source freshness
against a particular newsroom still requires independent evidence of its full-roster and
fresh-body delivery sequence.

## Bounded source synchronization version 2

Set `SOURCE_URL` to the same numeric loopback origin with `/v2/source-sync` to
select the new protocol. Deploy a compatible receiver first. New directories use
the existing initialization commands. Existing committed directories require an
explicit offline `--upgrade-source-sync` for each rundown and
`--upgrade-catalogue-sync` for the catalogue, using their exact original bindings
and the new URL. Include every retained member in `source-members.json`, including receipt-only
stores, as well as every explicitly configured rundown. The inventory is bound to source/peer/transport identity and stays
unchanged; the individual checkpoints enforce the publication destination. Each operation
takes the existing ownership lock, preserves
counters and original replay receipts, saves the exact old checkpoint as `.pre-v2`,
and exits without starting any transport. It does not migrate native protocol
state. Ordinary startup still invalidates source coverage before publishing.
The backup is recovery evidence; restoring it after new traffic would roll back
counters and receipts and is not a supported downgrade.

Delivery uses `start`, `missing`, `parts`, `commit` and `heartbeat` under that URL.
An ordered manifest references SHA-256 story objects. Large occurrence arrays,
manifest reference arrays and catalogues use ordered content-addressed parts too.
Each request is at most 64 KiB; each decoded transport part is at most 32 KiB.
Unchanged objects are reused, interrupted delivery resumes, and the receiver
publishes only after every required object is retained and validated. A published
unchanged revision renews through a small heartbeat. A newer pending revision
inhibits preparation; it never makes missing parts authoritative deletion.
Four workers give each enrolled show one bounded request per turn in both publication versions.
Newly enrolled members join that scheduler without a restart, even during an in-flight delivery.

V2 removes the legacy aggregate story, occurrence and catalogue byte limits while
preserving identity, ordering, field bounds and source freshness rules. Checkpoints
store immutable source records separately and atomically replace a small root;
a preparation receipt does not rewrite unchanged story content. Input application
still stages the complete logical rundown and scans retained receipts. Very large
receipt histories and sustained ingress remain qualification concerns. MOS framing
and per-field limits are unchanged. The 512 retained-store ceiling matches the existing
serialized discovery bound and includes explicit, inactive and receipt-only members. Neither source
acceptance nor the synthetic capacity checks qualify a destination's physical capacity or
rendering.
