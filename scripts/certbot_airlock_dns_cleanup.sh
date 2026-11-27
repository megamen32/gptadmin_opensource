#!/usr/bin/env bash
# Remove exactly the dedicated DNS-01 value after validation using the M1's
# authenticated SpaceWeb client. Its MCP resolves the provider row index
# before issuing editTxt deletion.
set -Eeuo pipefail

: "${CERTBOT_DOMAIN:?certbot must supply CERTBOT_DOMAIN}"
: "${CERTBOT_VALIDATION:?certbot must supply CERTBOT_VALIDATION}"

zone="bezrabotnyi.com"
domain="${CERTBOT_DOMAIN#\*.}"
suffix=".${zone}"
if [[ "$domain" != *"$suffix" ]]; then
  echo "Unsupported DNS-01 domain: $CERTBOT_DOMAIN" >&2
  exit 2
fi

relative_name="${domain%"$suffix"}"
record_name="_acme-challenge${relative_name:+.$relative_name}"

if ! sudo -n -u admin env HOME=/home/admin ssh \
  -o BatchMode=yes \
  -o ConnectTimeout=12 \
  -o StrictHostKeyChecking=yes \
  -p 2222 \
  user@127.0.0.1 \
  /usr/bin/python3 \
  /Users/user/plugins/sweb-dns-mcp/scripts/certbot_sweb_dns.py \
  del "$record_name" "$CERTBOT_VALIDATION" >/dev/null; then
  echo "Could not remove the DNS-01 TXT record through the Mac SpaceWeb client" >&2
  exit 1
fi

echo "Removed DNS-01 TXT record for ${CERTBOT_DOMAIN}" >&2
