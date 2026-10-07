#!/bin/sh
# xivstream: runs inside the session's gamescope, which shows the launcher and
# then the game fullscreen at the client's resolution. Looped
# because the stream is the only way in: XIVLauncher exits when the game does.
# (The Incus session's /usr/local/lib/xivstream/launcher, for Wolf.)
export DXVK_FRAME_RATE="${DXVK_FRAME_RATE:-60}"
# FFXIV's RGBA swapchain is rejected by Gamescope's WSI bypass on the P4000.
# Use the normal Xwayland Vulkan presentation path inside Gamescope instead.
export DISABLE_GAMESCOPE_WSI=1
while :; do
    /opt/xivlauncher/XIVLauncher.Core
    sleep 2
done
