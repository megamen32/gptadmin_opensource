# GPTAdmin ShellMCP for Android / Termux

This package contains only the ShellMCP reliable transport for Android.
It is intended to run in Termux in long-poll mode; the GPTAdmin hub stays on your server.

Install:

    curl -fsSL https://became.bezrabotnyi.com/install_android.sh | bash

Optional Shizuku/rish privilege mode:

    # Default is auto. Export rish/rish_shizuku.dex from Shizuku into Termux
    # and restart ShellMCP; explicit root/sudo shell_exec requests will use rish.
    curl -fsSL https://became.bezrabotnyi.com/install_android.sh | bash

Modes:

- auto: normal Termux shell_exec; root/sudo requests use rish when available.
- none: normal Termux shell_exec only.
- shizuku: force explicit root/sudo shell_exec requests through rish.
- shizuku-all: every shell_exec runs through rish.

