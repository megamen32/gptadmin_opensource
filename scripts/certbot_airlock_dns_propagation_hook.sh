#!/usr/bin/env bash
# Reuse the authoritative propagation guard with Airlock's persistent
# SpaceWeb publication hook.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
export DNS_AUTH_HOOK="$script_dir/certbot_airlock_dns_auth.sh"
exec "$script_dir/certbot_dns_propagation_hook.sh"
