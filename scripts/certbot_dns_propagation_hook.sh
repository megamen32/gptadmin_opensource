#!/usr/bin/env bash
# Wrap an existing DNS auth hook; do not validate while its old RRset is cached.
set -Eeuo pipefail

: "${CERTBOT_DOMAIN:?certbot must supply CERTBOT_DOMAIN}"
: "${CERTBOT_VALIDATION:?certbot must supply CERTBOT_VALIDATION}"
# Defaults preserve unattended renewal on the existing SpaceWeb deployment:
# certbot saves the hook path, but does not save the issuing shell environment.
DNS_AUTH_HOOK="${DNS_AUTH_HOOK:-/root/certbot-sweb-auth.sh}"
DNS_AUTHORITIES="${DNS_AUTHORITIES:-ns1.spaceweb.ru ns2.spaceweb.ru ns3.spaceweb.pro ns4.spaceweb.pro}"

name="_acme-challenge.${CERTBOT_DOMAIN#\*.}"
read -r -a authorities <<< "$DNS_AUTHORITIES"
ttl_floor="${DNS_PROPAGATION_TTL:-600}"
timeout="${DNS_PROPAGATION_TIMEOUT:-1200}"
[[ "$ttl_floor" =~ ^[0-9]+$ && "$timeout" =~ ^[0-9]+$ ]] || exit 2
(( timeout > ttl_floor )) || exit 2

# Capture the TTL BEFORE mutation. It bounds caches still holding the old RRset.
for server in "${authorities[@]}"; do
  answer=$(dig +time=3 +tries=1 +noall +answer "@$server" "$name" TXT)
  while read -r owner ttl rest; do
    if [[ "$ttl" =~ ^[0-9]+$ ]] && (( ttl > ttl_floor )); then
      ttl_floor=$ttl
    fi
  done <<< "$answer"
done
(( timeout > ttl_floor )) || { echo 'DNS TTL exceeds propagation timeout' >&2; exit 1; }

"$DNS_AUTH_HOOK"

started=$SECONDS
visible_since=-1
echo "Waiting at least ${ttl_floor}s for DNS caches for $name" >&2
while (( SECONDS - started < timeout )); do
  ready=true
  missing=false
  for server in "${authorities[@]}"; do
    if ! answer=$(dig +time=3 +tries=1 +short "@$server" "$name" TXT); then
      # An unavailable observation is not evidence that a published record
      # disappeared. Still require every authority to answer before success.
      ready=false
      continue
    fi
    if ! grep -Fxq -- "\"$CERTBOT_VALIDATION\"" <<< "$answer"; then
      ready=false
      missing=true
    fi
  done
  if $ready; then
    if (( visible_since < 0 )); then visible_since=$SECONDS; fi
    if (( SECONDS - visible_since >= ttl_floor )); then
      echo "DNS challenge visible on all configured authorities; old RRset TTL elapsed" >&2
      exit 0
    fi
  elif $missing; then
    visible_since=-1
  fi
  sleep 10
done
echo "DNS challenge propagation timed out for $name" >&2
exit 1
