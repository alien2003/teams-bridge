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
$tasks = & schtasks.exe /query /fo csv /nh 2>$null | ForEach-Object { ($_ -split '","')[0].Trim('"') } | Where-Object { $_ -and $_ -ne 'TaskName' } | Sort-Object -Unique -CaseSensitive
[IO.File]::WriteAllLines((Join-Path $OutDir "$Label.tasks.txt"), [string[]]$tasks, $enc)
Write-Output ("snapshot {0}: {1} entries, {2} tasks" -f $Label, $sorted.Count, $tasks.Count)
