#!/bin/bash

set -u
set -e

source ${BR2_EXTERNAL_AA_PROXY_OS_PATH}/board/common/post-build.sh
source ${BR2_EXTERNAL_AA_PROXY_OS_PATH}/board/common/add_tty1.sh

# DeX probe (throwaway test image), Raspberry Pi 4 only:
# - bring Ethernet up with DHCP by default, so the board is reachable over a
#   home network for development without touching the web UI first
# - mark the login banner so the running image can be told apart
case "${BASE_DIR:-}" in
    */rpi4)
        CFG="${TARGET_DIR}/etc/aa-proxy-rs/config.toml"
        sed -i 's/^\([[:space:]]*\)eth_mode = ""/\1eth_mode = "DHCP"/' "${CFG}" || true
        grep -q 'eth_mode = "DHCP"' "${CFG}" || echo "WARNING: eth_mode default was not changed"
        echo "dex-v1 test image" >> "${TARGET_DIR}/etc/issue"
        ;;
esac
