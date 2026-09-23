# Build ai-guard unified binary for Windows
# Usage: .\build.ps1 [output_dir]

param(
    [string]$OutputDir = "$PSScriptRoot\bin"
)

$ErrorActionPreference = "Stop"

Write-Host "Building ai-guard for windows/amd64..."
Write-Host "Output: $OutputDir\"
Write-Host ""

# Create output directory
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

Set-Location $PSScriptRoot

Write-Host "  Building ai-guard... " -NoNewline
try {
    go build -o "$OutputDir\ai-guard.exe" ".\cmd\ai-guard"
    Write-Host "✅"
} catch {
    Write-Host "❌"
    throw
}

Write-Host ""
Write-Host "✅ Build successful!"
Write-Host ""
Get-ChildItem $OutputDir | Format-Table Name, Length
Write-Host ""
Write-Host "Quick start:"
Write-Host "  $OutputDir\ai-guard.exe project-assess `"构建一个 REST API`""
Write-Host "  $OutputDir\ai-guard.exe code-quality-gate"
Write-Host "  $OutputDir\ai-guard.exe verify-task T-001"
