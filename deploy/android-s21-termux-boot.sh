#!/system/bin/sh
# Installed as the existing Magisk service.d launcher on the managed S21.
set -eu
while [ "$(getprop sys.user.0.ce_available)" != true ]; do
    sleep 2
done

termux=/data/data/com.termux
launcher=$termux/files/home/.local/share/gptadmin/run-shellmcp.sh
[ -x "$launcher" ] || exit 0
uid=$(stat -c %u "$termux")
[ "$uid" -ge 10000 ] || exit 1

# Magisk su drops supplementary groups. Termux has Android's INTERNET grant
# (AID_INET, 3003), which Bionic needs to resolve names through netd.
exec su -g "$uid" -G 3003 "$uid" -c '
    export PREFIX=/data/data/com.termux/files/usr
    export HOME=/data/data/com.termux/files/home
    export PATH=$PREFIX/bin:$PATH
    export LD_PRELOAD=$PREFIX/lib/libtermux-exec.so
    cd "$HOME"
    exec "$PREFIX/bin/bash" "$HOME/.local/share/gptadmin/run-shellmcp.sh"
'
