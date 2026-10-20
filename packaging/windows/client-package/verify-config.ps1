param(
  [Parameter(Mandatory = $true)]
  [string]$Path
)

$ErrorActionPreference = 'Stop'
try {
  $config = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
  $publicKey = [Convert]::FromBase64String([string]$config.adminPub)
  if ($publicKey.Length -ne 32) {
    throw 'adminPub must be a base64-encoded Ed25519 public key.'
  }
} catch {
  Write-Error "Invalid client configuration: $($_.Exception.Message)"
  exit 1
}

exit 0
