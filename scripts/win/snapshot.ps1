# Snapshot of the host areas the brief tells us to watch (BRIEF §1.5).
# Writes <OutDir>\<Label>.files.tsv and <OutDir>\<Label>.tasks.txt (UTF-8, sorted).
# Read-only apart from those two files, which live in the sandbox.
param(
  [Parameter(Mandatory = $true)][string]$OutDir,
  [Parameter(Mandatory = $true)][string]$Label,
  [Parameter(Mandatory = $true)][string]$Sandbox
)
$ErrorActionPreference = 'SilentlyContinue'
$roots = @(
  @{ Name = 'USERPROFILE';  Path = $env:USERPROFILE;  Depth = 0 },
  @{ Name = 'LOCALAPPDATA'; Path = $env:LOCALAPPDATA; Depth = 2 },
  @{ Name = 'APPDATA';      Path = $env:APPDATA;      Depth = 2 },
  @{ Name = 'TEMP';         Path = $env:TEMP;         Depth = 0 }
)
$sandboxFull = [IO.Path]::GetFullPath($Sandbox).TrimEnd('\')
$lines = New-Object System.Collections.Generic.List[string]
foreach ($r in $roots) {
  $base = [IO.Path]::GetFullPath($r.Path).TrimEnd('\')
  Get-ChildItem -LiteralPath $base -Force -Recurse -Depth $r.Depth -ErrorAction SilentlyContinue | ForEach-Object {
    $full = $_.FullName
    if ($full.StartsWith($sandboxFull, [StringComparison]::OrdinalIgnoreCase)) { return }
    $rel = $full.Substring($base.Length).TrimStart('\')
    if ($_.PSIsContainer) { $kind = 'd'; $size = '-' } else { $kind = 'f'; $size = $_.Length }
    $lines.Add(('{0}\{1}' -f $r.Name, $rel) + "`t" + $kind + "`t" + $size + "`t" + $_.LastWriteTimeUtc.ToString('o'))
  }
}
$sorted = $lines | Sort-Object -CaseSensitive
$enc = New-Object System.Text.UTF8Encoding($false)
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
[IO.File]::WriteAllLines((Join-Path $OutDir "$Label.files.tsv"), [string[]]$sorted, $enc)
# Registry: HKCU\Software key names (2 levels) and, recursively, every value under the keys a
# Chromium/Edge launch is known to touch. Values are recorded as name=sha256(data) to keep secrets out.
$reg = New-Object System.Collections.Generic.List[string]
$sha = [Security.Cryptography.SHA256]::Create()
function Add-RegKey([string]$path, [int]$depth, [bool]$values) {
  $k = Get-Item -LiteralPath $path -ErrorAction SilentlyContinue
  if (-not $k) { return }
  $reg.Add("K`t" + $k.Name)
  if ($values) {
    foreach ($n in $k.GetValueNames()) {
      $v = $k.GetValue($n, $null, 'DoNotExpandEnvironmentNames')
      if ($v -is [byte[]]) { $bytes = $v } else { $bytes = [Text.Encoding]::UTF8.GetBytes([string]($v -join "`n")) }
      $h = ([BitConverter]::ToString($sha.ComputeHash($bytes)) -replace '-', '').Substring(0, 16)
      $reg.Add("V`t" + $k.Name + "`t" + $n + "`t" + $h)
    }
  }
  if ($depth -gt 0) {
    foreach ($c in (Get-ChildItem -LiteralPath $path -ErrorAction SilentlyContinue)) { Add-RegKey $c.PSPath ($depth - 1) $values }
  }
}
Add-RegKey 'HKCU:\Software' 2 $false
foreach ($p in @('HKCU:\Software\Microsoft\Edge', 'HKCU:\Software\Microsoft\EdgeUpdate', 'HKCU:\Software\Microsoft\Edge Beta',
                 'HKCU:\Software\Classes\MSEdgeHTM', 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run')) {
  Add-RegKey $p 20 $true
}
[IO.File]::WriteAllLines((Join-Path $OutDir "$Label.reg.tsv"), [string[]]($reg | Sort-Object -Unique -CaseSensitive), $enc)

$tasks = & schtasks.exe /query /fo csv /nh 2>$null | ForEach-Object { ($_ -split '","')[0].Trim('"') } | Where-Object { $_ -and $_ -ne 'TaskName' } | Sort-Object -Unique -CaseSensitive
[IO.File]::WriteAllLines((Join-Path $OutDir "$Label.tasks.txt"), [string[]]$tasks, $enc)
Write-Output ("snapshot {0}: {1} entries, {2} registry lines, {3} tasks" -f $Label, $sorted.Count, $reg.Count, $tasks.Count)
