#!/usr/bin/env bash
# Publish the DNS-01 value through the authenticated SpaceWeb client on the
# M1. Certbot invokes this as root; the existing node-100 reverse SSH bridge
# keeps the panel credentials on the machine where they are maintained.
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

call_spaceweb() {
  sudo -n -u admin env HOME=/home/admin ssh \
    -o BatchMode=yes \
    -o ConnectTimeout=12 \
    -o StrictHostKeyChecking=yes \
    -p 2222 \
    user@127.0.0.1 \
    /usr/bin/python3 \
    /Users/user/plugins/sweb-dns-mcp/scripts/certbot_sweb_dns.py \
    "$@"
}

if ! response="$(call_spaceweb add "$record_name" "$CERTBOT_VALIDATION")"; then
  echo "Could not publish the DNS-01 TXT record through the Mac SpaceWeb client" >&2
  exit 1
fi

if ! jq -e '.result == true' <<<"$response" >/dev/null; then
  echo "SpaceWeb did not accept the DNS-01 TXT record" >&2
  exit 1
fi

echo "Published DNS-01 TXT record for ${CERTBOT_DOMAIN}" >&2
