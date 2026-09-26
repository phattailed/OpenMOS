# OpenMOS

OpenMOS is a small Media Object Server implementation with one MOS message core and two receive transports.

| Transport | Framing | Envelope and response |
| --- | --- | --- |
| MOS 2.8.4 TCP | UCS-2 big-endian XML stream on the configured receive port | Requires `<mos>`, `mosID`, and `ncsID`; expects `messageID` but tolerates a missing inbound ID for compatibility and echoes supplied IDs in replies. |
| MOS 4 WebSocket | Binary UCS-2 big-endian frames on `/mos?mosID=...&ncsID=...&channel=ro` | Requires the same identities and a `messageID` except on `keepAlive`; replies echo the request ID. |

Both transports process `roCreate` through the same service and send `roAck` only after storage succeeds. A retry with the same message ID replays the original response without applying the operation again. The in-memory retry record is bounded and does not survive a process restart. Nested `mosExternalMetadata` is stored as opaque XML with its scope and schema. MOS 2.x TCP also answers `roReqAll` with `roListAll` summaries.

Profile 0 handles `keepAlive` (no reply), `heartbeat` (correlated reply with reflection protection), `reqMachInfo`, and `listMachInfo`. Machine info advertises Profile 0 only. Other profiles are not claimed. Local tests verify protocol framing and message handling; they do not establish interoperability with a live NCS.

## Run

From `src/`:

```sh
go build ./...
go test ./...
go run . --generate-config=config.yaml
go run . --config=config.yaml
```

The default configuration enables MOS 2.x TCP on port 10541 and keeps MOS 4 WebSocket disabled. Set `WS_ENABLED=true` and `WS_PORT` to enable WebSocket; configure TLS certificate and key paths for a secure listener. `MOS_ID` sets the local identity, and optional `MOS_NCS_ID` restricts the accepted peer identity. WebSocket upgrades currently identify peers by URL parameters, so restrict listener access to trusted peers. The in-memory retry record is not durable.

The protocol sources and implementation boundaries are described in [the protocol synthesis](doc/mos-protocol-source-synthesis.md). See [LICENSE](LICENSE) for licensing.
