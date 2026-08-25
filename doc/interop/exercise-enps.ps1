$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

# Read-only exercise of the ENPS-side MOS 3.x and MOS 4 endpoints.
# Only Profile 0 query messages are sent (heartbeat / reqMachInfo) - these are
# pure confidence/discovery messages and change no NCS state.

$utf8   = New-Object System.Text.UTF8Encoding($false)
$ucs2be = New-Object System.Text.UnicodeEncoding($true, $false)

function Invoke-Mos4 {
    param([string]$Label, [string]$Channel, [string]$Xml, [string]$Encoding = 'utf8')

    Write-Output ('===== ' + $Label + ' (channel=' + $Channel + ', enc=' + $Encoding + ') =====')
    $url = 'ws://127.0.0.1/' + $MOS4_PATH + '/?mosID=' + $MOS_ID + '&ncsID=' + $NCS_ID + '&channel=' + $Channel
    $ws  = New-Object System.Net.WebSockets.ClientWebSocket
    $cts = New-Object System.Threading.CancellationTokenSource
    $cts.CancelAfter(15000)
    try {
        $t = $ws.ConnectAsync([Uri]$url, $cts.Token); $t.Wait(12000) | Out-Null
        if ($ws.State -ne 'Open') {
            Write-Output ('CONNECT FAILED: state=' + $ws.State)
            return
        }
        Write-Output 'CONNECTED (HTTP 101)'

        if ($Encoding -eq 'utf8') {
            $bytes = $utf8.GetBytes($Xml); $msgType = 'Text'
        } else {
            $bytes = $ucs2be.GetBytes($Xml); $msgType = 'Binary'
        }
        $seg = New-Object System.ArraySegment[byte] (,$bytes)
        $ws.SendAsync($seg, $msgType, $true, $cts.Token).Wait(8000) | Out-Null
        Write-Output ('SENT ' + $msgType + ' frame, ' + $bytes.Length + ' bytes')

        $buf = New-Object byte[] 32768
        $rseg = New-Object System.ArraySegment[byte] (,$buf)
        $rt = $ws.ReceiveAsync($rseg, $cts.Token)
        if ($rt.Wait(10000)) {
            $res = $rt.Result
            if ($res.MessageType -eq 'Close') {
                Write-Output ('SERVER CLOSED: ' + $ws.CloseStatus + ' / ' + $ws.CloseStatusDescription)
            } else {
                $raw = $buf[0..($res.Count-1)]
                $asUtf8 = $utf8.GetString($raw)
                $asUcs2 = $ucs2be.GetString($raw)
                $pick = if ($asUtf8 -match '<mos') { $asUtf8 } elseif ($asUcs2 -match '<mos') { $asUcs2 } else { $asUtf8 }
                $pick = ($pick -replace '\s+',' ').Trim()
                Write-Output ('RECEIVED (' + $res.MessageType + ', ' + $res.Count + ' bytes):')
                Write-Output $pick
            }
        } else {
            Write-Output 'NO RESPONSE within 10s'
        }
    } catch {
        Write-Output ('ERROR: ' + $_.Exception.GetBaseException().Message)
    } finally {
        try { if ($ws.State -eq 'Open') { $ws.CloseAsync('NormalClosure','done',[Threading.CancellationToken]::None).Wait(3000) | Out-Null } } catch {}
        $ws.Dispose()
    }
    Write-Output ''
}

# Site-specific values. Override via environment.
#   MOS_ID     MOS ID registered for OpenMOS on this NCS
#   NCS_ID     NCS ID this ENPS presents
#   MOS4_PATH  URL path of the NCS MOS 4 WebSocket endpoint
#   MOS3_PORT  port serving the MOS 3.x WebService
$MOS_ID    = if ($env:MOS_ID)    { $env:MOS_ID }    else { 'openmos.test.mos' }
$NCS_ID    = if ($env:NCS_ID)    { $env:NCS_ID }    else { 'NCS-HOST' }
$MOS4_PATH = if ($env:MOS4_PATH) { $env:MOS4_PATH } else { 'MOS4NCS' }
$MOS3_PORT = if ($env:MOS3_PORT) { $env:MOS3_PORT } else { '10543' }

$reqMachInfo = "<mos><mosID>$MOS_ID</mosID><ncsID>$NCS_ID</ncsID><messageID>9001</messageID><reqMachInfo/></mos>"
$heartbeat   = "<mos><mosID>$MOS_ID</mosID><ncsID>$NCS_ID</ncsID><messageID>9002</messageID><heartbeat><time>2026-08-24T18:10:00</time></heartbeat></mos>"

Write-Output ('ENPS-SIDE EXERCISE from ' + $env:COMPUTERNAME + ' at ' + (Get-Date -Format 'o'))
Write-Output ''

Invoke-Mos4 -Label 'M01 reqMachInfo -> expect listMachInfo' -Channel 'ro'  -Xml $reqMachInfo -Encoding 'utf8'
Invoke-Mos4 -Label 'M02 reqMachInfo UCS-2BE binary frame'   -Channel 'ro'  -Xml $reqMachInfo -Encoding 'ucs2'
Invoke-Mos4 -Label 'M03 heartbeat -> expect heartbeat'      -Channel 'ro'  -Xml $heartbeat   -Encoding 'utf8'
Invoke-Mos4 -Label 'M04 reqMachInfo on mom channel'         -Channel 'mom' -Xml $reqMachInfo -Encoding 'utf8'
Invoke-Mos4 -Label 'M05 aux channel accepted?'              -Channel 'aux' -Xml $reqMachInfo -Encoding 'utf8'

# --- MOS 3.8.4 SOAP endpoint discovery ---
Write-Output '===== M06 MOS 3.8.4 SOAP endpoint discovery ====='
$base = 'http://127.0.0.1:' + $MOS3_PORT + '/MOS384/'
$cands = @(
  $base,
  ($base + 'MOSWebService.asmx'),
  ($base + 'MOSWebService.asmx?wsdl'),
  ($base + 'MOSWebService.svc'),
  ($base + 'MOSWebService.svc?wsdl'),
  ($base + 'mos.asmx?wsdl')
)
foreach ($u in $cands) {
    try {
        $r = Invoke-WebRequest -Uri $u -UseBasicParsing -TimeoutSec 8 -Method Get
        Write-Output ('  ' + [int]$r.StatusCode + '  ' + $u + '   ct=' + $r.Headers['Content-Type'] + ' len=' + $r.RawContentLength)
    } catch {
        $resp = $_.Exception.Response
        $code = if ($resp -ne $null) { [int]$resp.StatusCode } else { 'ERR' }
        Write-Output ('  ' + $code + '  ' + $u)
    }
}
Write-Output ''
Write-Output 'ENPS-SIDE EXERCISE COMPLETE'
