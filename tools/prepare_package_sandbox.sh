#!/usr/bin/env bash
# Only the disposable GitHub-hosted package-test VM: give its trusted bwrap
# binary a named userns profile, retaining Ubuntu's system-wide restrictions.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || {
    echo 'This setup is only for a disposable GitHub-hosted runner.' >&2
    exit 1
}
restriction=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
if [[ -r "$restriction" && $(cat "$restriction") != 0 ]]; then
    sandbox_bin=/usr/local/lib/xivstream-ci/bwrap
    sudo install -D -m 0755 /usr/bin/bwrap "$sandbox_bin"
    sudo tee /etc/apparmor.d/xivstream-ci-bwrap >/dev/null <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile xivstream-ci-bwrap /usr/local/lib/xivstream-ci/bwrap flags=(unconfined) {
    userns,
}
PROFILE
    sudo apparmor_parser --replace /etc/apparmor.d/xivstream-ci-bwrap
    echo /usr/local/lib/xivstream-ci >> "$GITHUB_PATH"
else
    sandbox_bin=/usr/bin/bwrap
fi
"$sandbox_bin" --unshare-all --die-with-parent --ro-bind / / /bin/true
echo 'Package-test namespace probe passed.'
