$ErrorActionPreference = 'Continue'

# MOS 2.x exercise: NCS host -> ssh reverse tunnel :20541 -> OpenMOS 127.0.0.1:10541
# MOS 2.8 requires UCS-2 big endian on the wire.
$enc = New-Object System.Text.UnicodeEncoding($true, $false)
$PORT = 20541

function Invoke-MosSession {
    # Sends one or more frames on a SINGLE socket, reading a response after each.
    param([string]$Label, [string[]]$Frames, [switch]$Pipelined)

    Write-Output ('===== ' + $Label + ' =====')
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $client.Connect('127.0.0.1', $PORT)
        $stream = $client.GetStream()
        $stream.ReadTimeout = 6000

        if ($Pipelined) {
            # All frames in ONE write to exercise the framer.
            $joined = ($Frames -join '')
            $bytes = $enc.GetBytes($joined)
            $stream.Write($bytes, 0, $bytes.Length)
            $stream.Flush()
            Write-Output ('SENT: ' + $Frames.Count + ' frames in a single write (' + $bytes.Length + ' bytes UCS-2BE)')
            for ($i = 0; $i -lt $Frames.Count; $i++) {
                Write-Output ('  <- response ' + ($i+1) + ': ' + (Read-Frame $stream))
            }
        } else {
            $n = 1
            foreach ($f in $Frames) {
                $bytes = $enc.GetBytes($f)
                try {
                    $stream.Write($bytes, 0, $bytes.Length)
                    $stream.Flush()
                } catch {
                    Write-Output ('  frame ' + $n + ' WRITE FAILED (server likely closed): ' + $_.Exception.Message)
                    break
                }
                Write-Output ('  frame ' + $n + ' -> ' + (Read-Frame $stream))
                $n++
            }
        }
    } catch {
        Write-Output ('CONNECT ERROR: ' + $_.Exception.Message)
    } finally {
        $client.Close()
    }
    Write-Output ''
}

function Read-Frame {
    param($stream)
    $buf = New-Object byte[] 16384
    try {
        $n = $stream.Read($buf, 0, $buf.Length)
    } catch {
        return ('NO RESPONSE (' + $_.Exception.Message.Split([char]10)[0] + ')')
    }
    if ($n -le 0) { return 'CLOSED BY SERVER (0 bytes)' }
    $txt = $enc.GetString($buf, 0, $n)
    $txt = $txt -replace '<\?xml[^>]*\?>', ''
    $txt = ($txt -replace '\s+', ' ').Trim()
    if ($txt.Length -gt 300) { $txt = $txt.Substring(0,300) + '...' }
    return $txt
}

# Must match the OpenMOS config under test. Override via environment.
$MOS = if ($env:MOS_ID)  { $env:MOS_ID }  else { 'openmos.test.mos' }
$NCS = if ($env:NCS_ID)  { $env:NCS_ID }  else { 'ncs.test.mos' }

function New-Frame {
    param([string]$MsgId, [string]$Body, [string]$MosOverride)
    $m = if ($MosOverride) { $MosOverride } else { $MOS }
    $idTag = if ($MsgId -eq '') { '' } else { "<messageID>$MsgId</messageID>" }
    return "<mos><mosID>$m</mosID><ncsID>$NCS</ncsID>$idTag$Body</mos>"
}

function RoCreate {
    param([string]$RoId, [string]$Slug = 'Exercise RO')
    return @"
<roCreate><roID>$RoId</roID><roSlug>$Slug</roSlug><roEdDur>00:01:00</roEdDur>
<story><storyID>$RoId-S1</storyID><storySlug>Story one</storySlug>
<item><itemID>1</itemID><itemSlug>Item one</itemSlug><objID>OBJ-1</objID><mosID>$MOS</mosID><itemEdDur>25</itemEdDur></item>
</story></roCreate>
"@
}

Write-Output ('MOS 2.x EXERCISE from ' + $env:COMPUTERNAME + ' at ' + (Get-Date -Format 'o'))
Write-Output ('target: 127.0.0.1:' + $PORT + ' (ssh -R tunnel to OpenMOS)')
Write-Output ''

# --- Profile 0 (mandatory per MOS spec) ---
Invoke-MosSession -Label 'T01 Profile 0 heartbeat' -Frames @(
    (New-Frame -MsgId '101' -Body '<heartbeat><time>2026-08-24T18:00:00</time></heartbeat>'))

Invoke-MosSession -Label 'T02 Profile 0 reqMachInfo' -Frames @(
    (New-Frame -MsgId '102' -Body '<reqMachInfo/>'))

Invoke-MosSession -Label 'T03 Profile 0 keepAlive (MOS4, no messageID per spec)' -Frames @(
    (New-Frame -MsgId '' -Body '<keepAlive/>'))

# --- Profile 2 running order construction ---
Invoke-MosSession -Label 'T04 roCreate with messageID' -Frames @(
    (New-Frame -MsgId '201' -Body (RoCreate -RoId 'EX-201')))

Invoke-MosSession -Label 'T05 roReplace' -Frames @(
    (New-Frame -MsgId '202' -Body ((RoCreate -RoId 'EX-201' -Slug 'Replaced slug') -replace 'roCreate','roReplace')))

Invoke-MosSession -Label 'T06 roStorySend' -Frames @(
    (New-Frame -MsgId '203' -Body '<roStorySend><roID>EX-201</roID><storyID>EX-201-S1</storyID><storySlug>Story one</storySlug><storyBody><p>Line one of copy.</p></storyBody></roStorySend>'))

Invoke-MosSession -Label 'T07 roDelete' -Frames @(
    (New-Frame -MsgId '204' -Body '<roDelete><roID>EX-201</roID></roDelete>'))

# --- Envelope / messageID semantics ---
Invoke-MosSession -Label 'T08 roCreate WITHOUT messageID (legal in MOS 2.8.4 DTD)' -Frames @(
    (New-Frame -MsgId '' -Body (RoCreate -RoId 'EX-NOMSGID')))

Invoke-MosSession -Label 'T09 messageID=0 (spec requires >=1)' -Frames @(
    (New-Frame -MsgId '0' -Body (RoCreate -RoId 'EX-ZERO')))

Invoke-MosSession -Label 'T10 messageID hex 0x12D (spec allows hex)' -Frames @(
    (New-Frame -MsgId '0x12D' -Body (RoCreate -RoId 'EX-HEX')))

Invoke-MosSession -Label 'T11 wrong mosID (should be refused)' -Frames @(
    (New-Frame -MsgId '205' -Body (RoCreate -RoId 'EX-WRONGMOS') -MosOverride 'someone.else.mos'))

# --- Idempotency / retry semantics (MOS 4.1.6 messageID purpose) ---
Invoke-MosSession -Label 'T12 duplicate messageID, identical content (retry -> should be idempotent)' -Frames @(
    (New-Frame -MsgId '301' -Body (RoCreate -RoId 'EX-DUP')),
    (New-Frame -MsgId '301' -Body (RoCreate -RoId 'EX-DUP')))

Invoke-MosSession -Label 'T13 duplicate messageID, DIFFERENT content (conflict -> should be rejected)' -Frames @(
    (New-Frame -MsgId '302' -Body (RoCreate -RoId 'EX-CONFLICT-A')),
    (New-Frame -MsgId '302' -Body (RoCreate -RoId 'EX-CONFLICT-B')))

# --- Transport behaviour ---
Invoke-MosSession -Label 'T14 socket reuse: two ro messages sequentially on ONE socket' -Frames @(
    (New-Frame -MsgId '401' -Body (RoCreate -RoId 'EX-REUSE')),
    (New-Frame -MsgId '402' -Body '<roDelete><roID>EX-REUSE</roID></roDelete>'))

Invoke-MosSession -Label 'T15 pipelined: two frames in a SINGLE write (framer test)' -Pipelined -Frames @(
    (New-Frame -MsgId '501' -Body (RoCreate -RoId 'EX-PIPE-1')),
    (New-Frame -MsgId '502' -Body (RoCreate -RoId 'EX-PIPE-2')))

Write-Output 'EXERCISE COMPLETE'
