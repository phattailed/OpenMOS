# Committed rundown source

OpenMOS can publish one retained rundown to an application over authenticated loopback
HTTP. This is an opt-in file-storage mode. It carries neutral source values; the receiving
application owns media selection, graphics mappings, external API identity and destination
effects. A MOS retention ACK, an HTTP source receipt and an applied destination effect are
three separate results.

The implementation has synthetic local tests. It does not add a MOS profile advertisement,
live newsroom proof, deployment qualification or a guarantee of physical rendering.

## Provisioning and operation

Select an unused state directory and configure these environment variables. The existing
MOS identity, peer identity and selected transport must also be configured and enabled.

```text
STORAGE_BACKEND=file
STATE_DIR=/private/source-state
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

The source retains current raw XML even when it exceeds the receiver's publication limits.
It then publishes `complete:false` with `stories:[]` and records the reason in the checkpoint's
`source.state.problem`. This suspends preparation while the receiver preserves its prior
associations. `complete:true` with an empty story list is an authoritative empty roster.
Structural ambiguity and unsupported wire shapes may be rejected with a NACK; a successful
retention ACK always means the accepted content is durably owned by OpenMOS.

Incoming messages with an identifier retain their input hash and original response without
eviction. Identical retries replay the original transport response and cannot refresh body
coverage. Changed content under the same input identifier is rejected. MOS 2.x permits no
message identifier; those inputs get atomic replacement but cannot claim retry deduplication.
Receipts accumulate while repository and raw source data keep only their latest state. The
checkpoint therefore has linear storage and rewrite cost in receipt count. An indexed receipt
store is deferred until measured volume justifies it; there is no automatic retirement,
counter reset, migration, broker or alternate source generation.

The synthetic retention check produced a checkpoint of about 745 KiB for 4098 compact
receipts and one empty story. Real response sizes and retained bodies determine the cost;
each commit rewrites the whole checkpoint. This is a deliberate one-source implementation,
with sustained-volume throughput still unqualified.

## Mixed order and local cue continuity

Body extraction follows XML document order across direct items, paragraph items, inline
text, paragraph-spanning commands and production instructions. Unsupported structure or a
command crossing an item boundary cannot certify order. Item occurrence identity uses the
source item ID within its source/rundown/story scope; it never uses an object ID or label.

Anonymous cues receive persisted local IDs. A local ID is retained for a payload edit only
when the complete mixed layout is unchanged, explicit item IDs stay in place, and each cue's
type, parsed verb and target form a unique unchanged selector within that story. Fields,
parameters and raw cue text remain editable payload. Repeated selectors, reordered slots,
changed selectors or previously unresolved alignment suspend completeness. This is a
conservative local continuity rule, not a guarantee of upstream cue identity. Authoritative
removal of every anonymous cue clears that baseline; later cues receive new IDs.

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

The publication limits are 100 stories, 200 total occurrences and 64 KiB of complete UTF-8
JSON. IDs, types, labels, raw timing, verbs, parameter keys, media roles and technical
descriptions allow 512 Unicode code points; abstracts, URLs, cue fields and parameter values
allow 2048. Raw cues and each metadata block allow 16384. Media, metadata, field arrays and
parameter maps allow 32 entries. Story IDs and mixed occurrence IDs must be unique in their
respective scopes. Revisions are positive and no greater than 9007199254740991. No field or
list is truncated to fit.

One publisher sends the exact latest committed body. A lost reply retries the same revision
and bytes; a newer committed revision supersedes pending older work. A late HTTP receipt
cannot mark a newer revision accepted. Only a matching `acceptedRevision`, `duplicate` and
`destinationApplied:false` response counts as source acceptance. Current retained authority
renews by identical duplicate every ten seconds for the receiver's thirty-second lease and
revalidates a restarted receiver. A missing source connection or expired inbound liveness
produces an incomplete revision. Receiver unavailability cannot block MOS retention; HTTP
409 halts publication without bumping or resetting the source counter.

Focused repository, service and actual TCP/WebSocket ingress tests cover these boundaries.
The normal Go build, vet, repeated test and race checks remain required. Source freshness
against a particular newsroom still requires independent evidence of its full-roster and
fresh-body delivery sequence.
