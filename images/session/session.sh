#!/bin/bash
# xivstream: the Wolf session, as the player, inside a session D-Bus
# (dbus-run-session, started by entrypoint.sh). Starts what the Incus
# session's user services did, then sway nested on Wolf's Wayland display.
set -u
LIB=/usr/local/lib/xivstream/session
log() { printf '[xivstream-session] %s\n' "$*" >&2; }

export XDG_SESSION_TYPE=wayland XDG_CURRENT_DESKTOP=sway XDG_SESSION_DESKTOP=sway
cd "$HOME" 2>/dev/null || cd /

if [ "${XIVSTREAM_SESSION_MODE:-game}" = game ]; then
    # The launcher keeps the account password through libsecret: an
    # empty-password login keyring, unlocked here, as the Incus session's
    # xivstream-keyring.service did (the keyring files are in the shared home).
    printf '' | gnome-keyring-daemon --replace --daemonize --unlock --components=secrets >/dev/null 2>&1 ||
        log "gnome-keyring did not start: the launcher will ask for the password"

    # The host's services at 127.0.0.1:<port>, as the Incus container's proxy
    # devices gave them: each port is forwarded to the bridge's gateway, where
    # a systemd socket unit relays it to its target (xivstream-fwd-*.socket).
    gateway=${XIVSTREAM_GATEWAY:-}
    if [ -z "$gateway" ]; then
        gateway=$(ip -4 route show default 2>/dev/null | awk '{print $3; exit}')
    fi
    # Each entry is <session port>:<gateway port> (the host listens high: see
    # config.WolfForward.GatewayPort).
    for fwd in ${XIVSTREAM_FORWARDS:-}; do
        port=${fwd%%:*} gwport=${fwd#*:}
        socat "TCP-LISTEN:$port,bind=127.0.0.1,reuseaddr,fork" "TCP:$gateway:$gwport" &
    done

    # The voice microphone: a tunnel source in Wolf's PulseAudio, replacing the
    # Incus session's PipeWire pulse-tunnel (60-alienware-mic.conf). That
    # PulseAudio runs in Wolf's container on the host network, so it dials the
    # gateway's forward, not this container's loopback. Its name says
    # "dualsense" so ghostty-voice picks it as a controller's microphone.
    if [ -n "${XIVSTREAM_MIC_PORT:-}" ] && [ -n "$gateway" ] && command -v pactl >/dev/null; then
        server="tcp:$gateway:$XIVSTREAM_MIC_PORT"
        if ! pactl list short modules 2>/dev/null | grep -q "module-tunnel-source.*server=$server"; then
            pactl load-module module-tunnel-source "server=$server" \
                "source=${XIVSTREAM_MIC_REMOTE_SOURCE:-alsa_input.usb-Sony_Interactive_Entertainment_DualSense_Wireless_Controller-00.Direct__Direct__source}" \
                source_name=xivstream-dualsense-mic rate=48000 channels=1 \
                "source_properties=device.description='DualSense microphone (voice-mic forward)'" >/dev/null ||
                log "could not load the voice microphone's tunnel source"
        fi
    fi
fi

# sway, nested on Wolf's compositor (it picks its Wayland backend by itself
# when WAYLAND_DISPLAY is set), sized to this client's stream as GoW's
# launch-comp.sh does (the refresh rate is Wolf's compositor's).
conf=$XDG_RUNTIME_DIR/xivstream-sway.conf
{
    printf 'output * resolution %sx%s position 0,0\n\n' "${GAMESCOPE_WIDTH:-1920}" "${GAMESCOPE_HEIGHT:-1080}"
    cat "$LIB/sway.conf"
    if [ "${XIVSTREAM_SESSION_MODE:-game}" = game ]; then
        printf '\nexec %s\nexec %s\n' "$LIB/ghostty-agent.sh" "$LIB/launcher.sh"
    else
        msg=${XIVSTREAM_MESSAGE//\"/\'}
        printf '\nexec swaynag -t warning -m "%s"\n' "$msg"
    fi
} >"$conf"
exec sway --unsupported-gpu -c "$conf"
