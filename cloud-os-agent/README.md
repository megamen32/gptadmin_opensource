# CloudOS status agents

These minimal agents only maintain a CloudOS computer registration and a
heartbeat. They do not receive filesystem, shell, process, or network-proxy
capabilities.

- `windows/heartbeat.ps1`: scheduled once per minute for Windows BeyondInfinity.
- `macos/heartbeat.sh`: launched every 30 seconds for the Mac mini user.

If the Hub restarts and forgets a computer ID, the next heartbeat creates a
new registration automatically.
