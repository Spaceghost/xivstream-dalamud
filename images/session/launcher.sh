#!/bin/sh
# xivstream: runs inside the session's sway. The launcher, and through it the
# game, as ordinary sway clients; sway's rules make them fullscreen. Looped
# because the stream is the only way in: XIVLauncher exits when the game does.
# (The Incus session's /usr/local/lib/xivstream/launcher, for Wolf.)
export DXVK_FRAME_RATE="${DXVK_FRAME_RATE:-60}"
while :; do
    /opt/xivlauncher/XIVLauncher.Core
    sleep 2
done
