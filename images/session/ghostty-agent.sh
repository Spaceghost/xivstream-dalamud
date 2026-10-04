#!/bin/sh
# xivstream: the in-session ghostty-agent (127.0.0.1:7777, the PTY server
# Ghostty for FFXIV talks to), as the Incus session ran it from its
# ghostty-agent.service override: the home's build with its own libraries,
# else the packaged one. Started beside gamescope, with Wolf's WAYLAND_DISPLAY, so the clipboard (wl-copy) works;
# restarted if it exits.
while :; do
    if [ -x "$HOME/.local/bin/ghostty-agent" ]; then
        LD_LIBRARY_PATH="$HOME/.local/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
            "$HOME/.local/bin/ghostty-agent" --listen 127.0.0.1:7777 --windows none --wayland-render-node none
    elif command -v ghostty-agent >/dev/null; then
        ghostty-agent --listen 127.0.0.1:7777 --windows none --wayland-render-node none
    else
        exit 0
    fi
    sleep 5
done
