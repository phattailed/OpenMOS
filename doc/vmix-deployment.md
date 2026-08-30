# Deploying the OpenMOS vMix Bridge

This guide is for the person deploying OpenMOS to feed ENPS rundown data into
vMix, and for the ENPS administrator who has to register it. It replaces the
manual step of copying stories and items from ENPS into a spreadsheet by hand:
OpenMOS receives the rundown from ENPS over the MOS protocol and serves it in a
shape vMix reads directly as a Data Source.

The whole flow:

```
ENPS ──MOS 2.x (TCP)──▶ OpenMOS + vMix bridge ──HTTP or CSV──▶ vMix Data Source
      (ENPS connects to us)                    (vMix polls us)
```

There are two sides to set up: the **OpenMOS host** (you) and the **ENPS device
registration** (the ENPS administrator). ENPS will not send anything until the
device is registered, so both sides must be done.

---

## Before you start: collect this information

You cannot register the device without these. Gather them first.

| Item | Where it comes from | Example |
|---|---|---|
| MOS ID for OpenMOS | You choose it; must be agreed with ENPS admin and match config exactly | `VMIX.BRIDGE.MOS` |
| NCS ID | The ENPS admin (the newsroom system's own ID) | `NEWSROOM.NCS` |
| OpenMOS host IP or hostname | The machine you run OpenMOS on; must be reachable from ENPS | `10.20.30.40` |
| MOS lower/upper ports | Protocol defaults, rarely changed | `10540` / `10541` |
| Rundowns to send | Which ENPS rundown(s) should reach vMix | "Evening News" |

> The MOS ID is the one detail that most commonly causes silent failures. ENPS
> rejects a connection whose MOS ID does not match the registered device
> **exactly**, including case. Agree on it in writing.

---

## Part 1 — OpenMOS host setup

1. **Build the executable** (Windows, no prerequisites needed):
   ```powershell
   powershell -ExecutionPolicy Bypass -File scripts\build.ps1
   ```
   This produces `dist\openmos.exe`.

2. **Generate a config:**
   ```powershell
   dist\openmos.exe --generate-config=config.yaml
   ```

3. **Edit `config.yaml`.** The settings that matter for a vMix deployment:
   ```yaml
   server:
       enabled: true          # MOS 2.x transport: ENPS connects here
       host: 0.0.0.0
       port: 10541
   mos:
       id: VMIX.BRIDGE.MOS    # MUST match what the ENPS admin registers
       ncsid: ""              # empty accepts any NCS ID; set it to lock down
   storage:
       backend: file          # keeps the rundown across restarts
   bridge:
       enabled: true          # turn the vMix bridge on
       httpenabled: true
       httpport: 8090
       csvenabled: false      # set true if vMix should read a file instead
       csvpath: rundown.csv
       fields: []             # empty = default columns; customise later
   ```

4. **Open the firewall** on the OpenMOS host for:
   - **inbound** TCP `10541` (and `10540` if ENPS uses the lower port) — from ENPS
   - **inbound** TCP `8090` — from the vMix machine (only if vMix polls over HTTP)

5. **Run it.** For a quick check, run in the foreground:
   ```powershell
   dist\openmos.exe --config=config.yaml
   ```
   For a permanent install, register it as an auto-start service (elevated prompt):
   ```powershell
   powershell -ExecutionPolicy Bypass -File scripts\install-service.ps1
   ```

6. **Confirm it is listening:** open `http://<openmos-host>:8090/healthz`. You
   should see `{"status":"ok","rows":0,...}`. Zero rows is expected until ENPS
   connects and sends a rundown.

---

## Part 2 — ENPS device registration (ENPS administrator)

This is done in the ENPS client by someone with system-maintenance rights. OpenMOS
is a listener: ENPS opens the connection to it, so ENPS must be told the device
exists and be pointed at the OpenMOS host.

Register a new MOS device with these fields:

| ENPS device field | Value | Notes |
|---|---|---|
| MOS ID | `VMIX.BRIDGE.MOS` | Must match `mos.id` in `config.yaml` exactly |
| Device host / IP | OpenMOS host address | Must be reachable from the ENPS server |
| Lower port | `10540` | Protocol default |
| Upper port | `10541` | Protocol default; where ENPS sends running orders |
| MOS Version | `2.8.4` | OpenMOS speaks MOS 2.x on this transport |
| Active / enabled | Yes | The device must be active to receive data |

Then, so rundown **content** actually flows:

1. **StorySend** — enable it for this device. This flag gates whether story and
   item content is sent to the device; it commonly defaults to **off**, and with
   it off the device connects but receives an empty or header-only rundown.
2. **MOS Control Active** — turn this **on** for each rundown that should reach
   vMix. This is set per rundown in its properties, and is what makes ENPS push
   that rundown (and its updates) to the registered MOS devices.

> If OpenMOS connects but `/healthz` stays at zero rows while a rundown is open in
> ENPS, the usual cause is **StorySend off** or **MOS Control Active off** on the
> rundown. Check those two first.

---

## Part 3 — Verify the connection

With both sides set up and a rundown open in ENPS with MOS Control Active on:

1. `http://<openmos-host>:8090/healthz` — the `rows` count should be greater than
   zero and the `generatedAt` time should update as you edit the rundown.
2. `http://<openmos-host>:8090/rundown.csv` — you should see a header row plus one
   row per rundown item.
3. Edit a story slug in ENPS; within a few seconds the change should appear in the
   endpoint output.

If you want to capture what ENPS actually sends (useful for troubleshooting or for
tuning the field mapping), set `capture.dir` in the config. **Note:** captured
frames contain the full body of news stories — treat that directory as holding
editorial content and do not share it.

---

## Part 4 — Hook up vMix

1. In vMix, open **Data Sources Manager** and add a source.
2. Choose the type matching the endpoint:
   - **CSV / Excel** → `http://<openmos-host>:8090/rundown.csv` (or the local file
     if you enabled `csvenabled`)
   - **JSON** → `http://<openmos-host>:8090/rundown.json`
   - **XML** → `http://<openmos-host>:8090/rundown.xml`
3. Set the refresh interval to how quickly vMix should reflect rundown changes.
4. Bind the columns to your title / GT inputs.

### Choosing which columns vMix gets

`bridge.fields` in `config.yaml` controls the output columns. Leave it empty for a
sensible default, or list exactly the fields your vMix templates need. Available
sources:

- Running order: `ro.slug`, `ro.channel`, `ro.duration`, `ro.status`, `ro.id`
- Story: `story.slug`, `story.number`, `story.presenter`, `story.duration`,
  `story.order`, `story.status`, `story.id`, `story.rawid`
- Item: `item.slug`, `item.objectid`, `item.duration`, `item.order`,
  `item.status`, `item.id`, `item.rawid`
- Any metadata attribute: `meta.<key>`
- Any verbatim `mosExternalMetadata` payload: `external.<schema>`

Because OpenMOS captures everything ENPS sends, you can decide which pieces vMix
uses at any time by editing `bridge.fields` and restarting — no code change.

---

## Troubleshooting quick reference

| Symptom | Likely cause | Fix |
|---|---|---|
| ENPS cannot connect | MOS ID mismatch, or firewall on `10541` | Match `mos.id` to the ENPS device; open the port |
| Connects, zero rows | StorySend off, or MOS Control Active off on the rundown | Enable both in ENPS |
| vMix shows nothing | Wrong Data Source URL or type, or `bridge.httpenabled` false | Check the URL and the config |
| Rows appear but wrong columns | `bridge.fields` does not match the vMix template | Adjust `bridge.fields`, restart |
| Data lost on restart | `storage.backend: memory` | Use `file` (default) or `mongo` |
