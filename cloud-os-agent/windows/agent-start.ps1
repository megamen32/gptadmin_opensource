$ErrorActionPreference = 'Stop'
if ((Get-NetTCPConnection -LocalPort 9137 -State Listen -ErrorAction SilentlyContinue)) { exit 0 }
& 'C:\Python312\python.exe' 'C:\Users\megam\AppData\Local\GPTAdminCloudOS\agent.py' --hub 'http://192.168.2.100:9001/api/v1/cloud-os' --name 'Windows BeyondInfinity' --os windows --listen '192.168.2.190:9137' --root 'C:\Users\megam' --state 'C:\Users\megam\AppData\Local\GPTAdminCloudOS\agent.json' *>> 'C:\Users\megam\AppData\Local\GPTAdminCloudOS\agent.log'
