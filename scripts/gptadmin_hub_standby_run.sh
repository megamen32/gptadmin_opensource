#!/bin/sh
set -eu
set -a
. /etc/gptadmin/gptadmin.env
set +a
export GPTADMIN_HUB_HOST=127.0.0.1
export GPTADMIN_HUB_PORT=19001
export HUB_PORT=19001
export HUB_URL=http://127.0.0.1:19001
export QUEUE_URL=http://127.0.0.1:19001/queue
export NO_PROXY=localhost,127.0.0.1
export no_proxy=localhost,127.0.0.1
exec /opt/gptadmin/bin/gptadmin_hub
