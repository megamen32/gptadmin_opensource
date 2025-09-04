# Simple installer for the rootd Windows agent.
# Generates a token and starts the agent in the background.

$token = ([System.Guid]::NewGuid().ToString('n'))
Write-Host "Generated ROOTD token: $token"

$env:ROOTD_TOKEN = $token
if (-not $env:HUB_URL) { $env:HUB_URL = "http://localhost:9001" }

pip install -r requirements.txt | Out-Null
Start-Process -WindowStyle Hidden python rootd_win.py

Write-Host "rootd Windows agent running and registered to $env:HUB_URL"
Write-Host "Use ROOTD_TOKEN=$token for authorization"
