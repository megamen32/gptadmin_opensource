# Cloud OS — GPTAdmin Computer Link

A Chrome extension that pairs your local computer with the Cloud OS session and manages the native host connection. Once connected, your computer appears as a filesystem, process, and network resource inside the Browser OS.

## How it works

```
┌─────────────┐     postMessage      ┌──────────────┐     chrome.runtime     ┌──────────────┐
│  Cloud OS    │ ◄──────────────────► │ content.js   │ ◄────────────────────► │ background.js│
│  (web page)  │   window.cloudOS     │ (injected)   │    .sendMessage()     │ (service     │
└─────────────┘                       └──────────────┘                        │  worker)     │
                                                                              │              │
                                                                     native  │              │  fetch
                                                                     messaging│              │  (heartbeat,
                                                                              │              │   pairing)
                                                                     ┌───────▼──────▼───────┐
                                                                     │   Native Host App    │
                                                                     │   (filesystem,       │
                                                                     │    processes, etc.)   │
                                                                     └──────────────────────┘
```

## Installation

### 1. Generate icons (one-time)

See [`icons/README.md`](icons/README.md) for instructions. You need `icon16.png`, `icon48.png`, and `icon128.png`.

### 2. Load the extension in Chrome

1. Open `chrome://extensions/`
2. Enable **Developer mode** (top-right toggle)
3. Click **Load unpacked**
4. Select this `browser-extension/` directory

### 3. Install the native host

The native host is a separate application that runs on the user's machine and performs actual filesystem/process operations.

1. Build or download the native host binary (`gptadmin-cloudos-nativehost`)
2. Place it at the path specified in `native-host-manifest.json` (default: `/usr/local/bin/gptadmin-cloudos-nativehost`)
3. Register the manifest with Chrome:

   **Linux:**
   ```bash
   sudo cp native-host-manifest.json \
     /etc/chromium-browser/native-messaging-hosts/com.gptadmin.cloudos.nativehost.json
   # or for Google Chrome:
   sudo cp native-host-manifest.json \
     /etc/opt/chrome/native-messaging-hosts/com.gptadmin.cloudos.nativehost.json
   ```

   **macOS:**
   ```bash
   sudo cp native-host-manifest.json \
     /Library/Google/Chrome/NativeMessagingHosts/com.gptadmin.cloudos.nativehost.json
   ```

   **Windows:**
   ```cmd
   reg add "HKCU\Software\Google\Chrome\NativeMessagingHosts\com.gptadmin.cloudos.nativehost" /ve /t REG_SZ /d "C:\path\to\native-host-manifest.json"
   ```

## Pairing flow

1. Click the extension icon → **Enable this Computer**
2. A 6-character pairing code appears in the popup
3. Enter the code on the Cloud OS hub page
4. The extension polls for confirmation (up to 5 minutes)
5. On confirmation, the computer transitions to **Connected** and heartbeats begin

## Configuration

| Setting | Location | Default |
|---------|----------|---------|
| Hub URL | Popup → ⚙ Settings | `https://became.bezrabotnyi.com` |
| Computer Name | Popup → Computer Name field | `My Computer` |
| Native Host Path | `native-host-manifest.json` → `path` | `/usr/local/bin/gptadmin-cloudos-nativehost` |

## `window.cloudOS` API

When the content script is injected on Cloud OS pages, it exposes:

```js
window.cloudOS.getComputers()                      // → Computer[]
window.cloudOS.readFile(computerId, path)           // → string
window.cloudOS.writeFile(computerId, path, content) // → void
window.cloudOS.execCommand(computerId, command)     // → { exitCode, stdout, stderr }
window.cloudOS.listFiles(computerId, path)          // → FileEntry[]
```

## Native host protocol

Communication uses Chrome's native messaging (JSON over stdio). Messages are newline-delimited JSON objects:

**Extension → Native host:**
```json
{"type": "fs.read", "path": "/home/user/file.txt", "requestId": "uuid"}
{"type": "fs.write", "path": "/home/user/file.txt", "content": "...", "requestId": "uuid"}
{"type": "fs.list", "path": "/home/user", "requestId": "uuid"}
{"type": "process.exec", "command": "ls -la", "requestId": "uuid"}
{"type": "system.info", "requestId": "uuid"}
```

**Native host → Extension:**
```json
{"type": "fs.read.response", "content": "...", "requestId": "uuid"}
{"type": "fs.list.response", "entries": [{"name": "file.txt", "type": "file", "size": 1234}], "requestId": "uuid"}
{"type": "process.exec.response", "exitCode": 0, "stdout": "...", "stderr": "", "requestId": "uuid"}
{"type": "system.info.response", "data": {"os": "linux", "arch": "x64", ...}, "requestId": "uuid"}
```

## File structure

```
browser-extension/
├── manifest.json              # MV3 extension manifest
├── background.js              # Service worker (pairing, heartbeat, routing)
├── popup.html                 # Popup UI (dark theme)
├── popup.js                   # Popup controller
├── content.js                 # Content script (window.cloudOS bridge)
├── native-host-manifest.json  # Native messaging host registration
├── icons/
│   ├── README.md              # Icon generation instructions
│   ├── icon16.png             # (generate from SVG)
│   ├── icon48.png             # (generate from SVG)
│   └── icon128.png            # (generate from SVG)
└── README.md                  # This file
```
