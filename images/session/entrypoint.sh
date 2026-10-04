#!/bin/bash
# xivstream: the Wolf session's entrypoint. Runs as root, prepares the
# container, then runs the session as the player (uid 1000) and stays as PID 1
# to pass a stop on.
#
# It never creates, renames or deletes a user or a home: /home/player is the
# shared game install, bind-mounted from the host (see the Containerfile).
#
# Environment from Wolf: XDG_RUNTIME_DIR (with Wolf's Wayland and PulseAudio
# sockets mounted in it), WAYLAND_DISPLAY, PULSE_SERVER, PUID/PGID (the
# client's run_uid/run_gid), GAMESCOPE_WIDTH/HEIGHT/REFRESH. From xivstream's
# app entry: DXVK_FRAME_RATE, XIVSTREAM_GATEWAY, XIVSTREAM_FORWARDS,
# XIVSTREAM_MIC_PORT.
set -uo pipefail

LIB=/usr/local/lib/xivstream/session
PLAYER_HOME=/home/player
LOCK=$PLAYER_HOME/.xlcore/.xivstream-game.lock

log() { printf '[xivstream-session] %s\n' "$*" >&2; }

if [ "${PUID:-1000}" != 1000 ] || [ "${PGID:-1000}" != 1000 ]; then
    log "this client's settings ask for uid ${PUID:-}:${PGID:-}; the shared home is 1000's, so the session runs as 1000"
fi

# NVIDIA: the driver volume Wolf mounts at /usr/nvidia, set up as GoW's Fedora
# base image does (cont-init.d/30-nvidia.sh): the libraries through
# /etc/ld.so.conf.d/nvidia.conf, then the ICD, EGL and GBM manifests.
if [ -d /usr/nvidia ]; then
    log "NVIDIA driver volume detected"
    ldconfig
    if [ -d /usr/nvidia/share/vulkan/icd.d ]; then
        mkdir -p /usr/share/vulkan/icd.d/
        cp /usr/nvidia/share/vulkan/icd.d/* /usr/share/vulkan/icd.d/
    fi
    if [ -d /usr/nvidia/share/egl/egl_external_platform.d ]; then
        mkdir -p /usr/share/egl/egl_external_platform.d/
        cp /usr/nvidia/share/egl/egl_external_platform.d/* /usr/share/egl/egl_external_platform.d/
    fi
    if [ -d /usr/nvidia/share/glvnd/egl_vendor.d ]; then
        mkdir -p /usr/share/glvnd/egl_vendor.d/
        cp /usr/nvidia/share/glvnd/egl_vendor.d/* /usr/share/glvnd/egl_vendor.d/
    fi
    if [ -d /usr/nvidia/lib/gbm ]; then
        mkdir -p /usr/lib64/gbm/
        cp /usr/nvidia/lib/gbm/* /usr/lib64/gbm/
    fi
fi

# Input: Wolf hot-plugs each pad later, as root, with
# `docker exec ... /bin/bash -c 'mkdir -p /dev/input && mknod ... && chmod 777 ...
# && fake-udev ...'`. Make what is already here usable the same way.
mkdir -p /dev/input
chmod 755 /dev/input
for node in /dev/input/* /dev/hidraw*; do
    [ -c "$node" ] && chmod 0666 "$node"
done

# The GPU's nodes keep the host's group ids: give the player those groups.
groups=
for node in /dev/dri/* /dev/nvidia* /dev/uinput /dev/uhid; do
    [ -c "$node" ] || continue
    gid=$(stat -c %g "$node")
    [ "$gid" = 0 ] && continue
    case ",$groups," in *",$gid,"*) ;; *) groups=${groups:+$groups,}$gid ;; esac
done

# Wolf's sockets are mounted into XDG_RUNTIME_DIR; the directory itself must
# be the player's (never recursively: the sockets are the host's files).
export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/1000}
mkdir -p "$XDG_RUNTIME_DIR"
chown 1000:1000 "$XDG_RUNTIME_DIR"
chmod 0700 "$XDG_RUNTIME_DIR"

# Refuse an empty home: without the real one, XIVLauncher would start a fresh
# install (a 90 GB download) into whatever directory is here.
mode=game
message=
if [ ! -f "$PLAYER_HOME/.xlcore/launcher.ini" ]; then
    mode=message
    message="xivstream: $PLAYER_HOME has no .xlcore/launcher.ini, so the game's home is not mounted. Not starting the launcher. On the host, check: systemctl status ${XIVSTREAM_HOME_UNIT:-var-lib-xivstream-home.mount}"
    log "$message"
else
    # One game per home: the lock is held for as long as anything this session
    # starts is alive (every process inherits descriptor 9). The Sunshine
    # fallback's launcher takes the same lock.
    if [ ! -e "$LOCK" ]; then
        install -o 1000 -g 1000 -m 0644 /dev/null "$LOCK"
    fi
    exec 9<>"$LOCK"
    if ! flock -n 9; then
        exec 9>&-
        mode=message
        message="FINAL FANTASY XIV is already running from another session (another Moonlight client, or the Sunshine fallback). Quit that session first, then reconnect here."
        log "the game lock is held elsewhere; showing a message instead of a second game"
    fi
fi
# Wolf removes the container (and its log) when a session ends: keep the
# session's output in the home as well.
if [ -d "$PLAYER_HOME/.xlcore/logs" ]; then
    exec > >(tee -a "$PLAYER_HOME/.xlcore/logs/xivstream-session.log") 2>&1
    log "session $(date -Is) for Wolf session ${WOLF_SESSION_ID:-?} (${GAMESCOPE_WIDTH:-?}x${GAMESCOPE_HEIGHT:-?})"
fi
export XIVSTREAM_SESSION_MODE=$mode XIVSTREAM_MESSAGE=$message
export HOME=$PLAYER_HOME USER=player LOGNAME=player SHELL=/bin/bash
export SWAYSOCK=$XDG_RUNTIME_DIR/xivstream-sway.sock

if [ -n "$groups" ]; then
    group_args=(--groups "$groups")
else
    group_args=(--clear-groups)
fi
setpriv --reuid=1000 --regid=1000 "${group_args[@]}" --inh-caps=-all --no-new-privs \
    dbus-run-session -- "$LIB/session.sh" &
child=$!

# Wolf stops the container with a two-second timeout (then SIGKILL). Ask the
# game window to close in case it is at the title screen; a logged-in game
# needs the player to log out first (see docs/backends.md).
stopping=0
on_stop() {
    [ "$stopping" = 1 ] && return
    stopping=1
    log "stop requested: closing the game window, then the session"
    if [ -S "$SWAYSOCK" ]; then
        swaymsg -s "$SWAYSOCK" '[title="^FINAL FANTASY XIV"] kill' >/dev/null 2>&1 || true
        sleep 1
    fi
    kill -TERM "$child" 2>/dev/null
}
trap on_stop TERM INT HUP

while kill -0 "$child" 2>/dev/null; do
    wait "$child"
done
wait "$child" 2>/dev/null
status=$?
log "session ended ($status)"
exit "$status"
