#!/usr/bin/env python3
"""Exercise a shipped Linux binary in a rootless, networkless fake host.

Usage: python3 tools/test_cpu_policy_package.py --binary ./xivstream
       --install-path /usr/bin/xivstream

Requires bubblewrap and Python 3. No sudo, host systemd/Incus, or game access.
The sandbox has a temporary /etc and only strict command fakes in /usr/bin.
There is deliberately no unsandboxed fallback when user namespaces are denied.
"""

import argparse
import json
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest


MOCK = r'''#!/usr/bin/python3
import json
from pathlib import Path
import sys

root = Path("/state")
state = json.loads((root / "state.json").read_text())
args = [Path(sys.argv[0]).name] + sys.argv[1:]
with (root / "commands.jsonl").open("a") as log:
    log.write(json.dumps(args) + "\n")
code, output = 0, ""
unit = "xivstream-cpu-policy.service"
if args == ["systemctl", "is-enabled", "--quiet", unit]:
    code = 0 if state["enabled"] else 1
elif args == ["systemctl", "is-active", "--quiet", unit]:
    code = 0 if state["active"] else 3
elif args == ["systemctl", "disable", "--now", unit]:
    if state.get("fail_disable", False):
        code = 1
    else:
        state.update(enabled=False, active=False)
elif args == ["systemctl", "daemon-reload"]:
    pass
elif args == ["systemctl", "enable", "--now", unit]:
    state.update(enabled=True, active=True)
elif args == ["systemctl", "restart", unit]:
    state["active"] = True
elif args == ["incus", "--force-local", "config", "get", "game-fixture", "limits.cpu"]:
    output = state["limit"]
elif (len(args) == 6 and args[:5] == ["incus", "--force-local", "config", "set", "game-fixture"]
      and args[5].startswith("limits.cpu=")):
    if state.get("fail_writes", 0):
        state["fail_writes"] -= 1
        code = 1
    else:
        state["limit"] = args[5][len("limits.cpu="):]
elif args == ["lscpu", "-e=CPU,CORE,SOCKET,ONLINE,MAXMHZ"]:
    output = "CPU CORE SOCKET ONLINE MAXMHZ\n0 0 0 yes 5000\n7 3 0 yes 5000"
else:
    print("FORBIDDEN command in CPU-only test: " + repr(args), file=sys.stderr)
    sys.exit(99)
(root / "state.json").write_text(json.dumps(state))
if output:
    print(output)
if code:
    print("fixture command failure", file=sys.stderr)
sys.exit(code)
'''


class CPUOnlyPackageTests(unittest.TestCase):
    binary: Path
    install_path: str

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="xivstream-cpu-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.etc = self.root / "etc"
        self.etc.mkdir()
        self.state_dir = self.root / "state"
        self.state_dir.mkdir()
        self.mock = self.root / "mock.py"
        self.mock.write_text(MOCK)
        self.mock.chmod(0o755)
        self.online = self.root / "online"
        self.online.write_text("0-7\n")
        self.write_state(enabled=True, active=True, limit="4-7")
        self.write_config()

    def write_state(self, **values):
        path = self.state_dir / "state.json"
        state = json.loads(path.read_text()) if path.exists() else {}
        state.update(values)
        path.write_text(json.dumps(state))

    def state(self):
        return json.loads((self.state_dir / "state.json").read_text())

    def commands(self):
        path = self.state_dir / "commands.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def write_config(self, busy="4-7", idle="0-7", static="0-7"):
        self.config = self.etc / "fixture.toml"
        self.config.write_text(
            'topology = "incus"\n[incus]\ncontainer = "game-fixture"\n'
            f'cpu = "{static}"\n[incus.cpu_policy]\nbusy_cpus = "{busy}"\nidle_cpus = "{idle}"\n'
        )

    def run_cli(self, *args, expected=0):
        command = [
            shutil.which("bwrap"), "--unshare-all", "--die-with-parent", "--new-session",
            "--uid", "0", "--gid", "0", "--clearenv",
            "--setenv", "PATH", "/usr/bin", "--setenv", "LANG", "C.UTF-8",
            "--ro-bind", "/usr", "/usr", "--tmpfs", "/var", "--dir", "/var/usrlocal",
            "--tmpfs", "/usr/bin", "--tmpfs", "/usr/local/bin",
            "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib",
            "--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev",
            "--tmpfs", "/tmp", "--tmpfs", "/run", "--tmpfs", "/sys",
            "--ro-bind", str(self.online), "/sys/devices/system/cpu/online",
            "--bind", str(self.etc), "/etc", "--bind", str(self.state_dir), "/state",
            "--ro-bind", str(Path(sys.executable).resolve()), "/usr/bin/python3",
            "--ro-bind", str(self.binary), self.install_path,
        ]
        for name in ("systemctl", "incus", "lscpu"):
            command.extend(["--ro-bind", str(self.mock), "/usr/bin/" + name])
        command.extend([self.install_path, *args, "--config", "/etc/fixture.toml"])
        result = subprocess.run(command, text=True, capture_output=True, timeout=20)
        self.assertNotIn("bwrap:", result.stderr, result.stdout + result.stderr)
        self.assertEqual(expected, result.returncode, result.stdout + result.stderr)
        self.assertNotIn("FORBIDDEN", result.stdout + result.stderr)
        return result.stdout + result.stderr

    def apply(self, expected=0):
        return self.run_cli("apply", "--cpu-policy-only", "--yes", expected=expected)

    def test_package_service_path_and_private_config(self):
        self.write_state(enabled=False, active=False)
        self.apply()
        unit = self.etc / "systemd/system/xivstream-cpu-policy.service"
        self.assertIn("ExecStart=" + self.install_path + " cpu-policy\n", unit.read_text())
        self.assertIn("# CPU policy configuration: ", unit.read_text())
        self.assertEqual(0o644, stat.S_IMODE(unit.stat().st_mode))
        self.assertEqual(0o600, stat.S_IMODE((self.etc / "xivstream/config.toml").stat().st_mode))
        self.assertTrue(self.state()["active"])
        self.assertTrue(self.state()["enabled"])

    def test_unchanged_apply_is_idempotent(self):
        self.apply()
        before = len(self.commands())
        self.apply()
        self.assertEqual([
            ["systemctl", "is-enabled", "--quiet", "xivstream-cpu-policy.service"],
            ["systemctl", "is-active", "--quiet", "xivstream-cpu-policy.service"],
        ], self.commands()[before:])

    def test_selection_cli_persists_and_reloads_only_policy(self):
        self.apply()
        unit = self.etc / "systemd/system/xivstream-cpu-policy.service"
        old = unit.read_bytes()
        self.run_cli("cpu-config", "--busy", "0,2,4,6", "--idle", "0-7")
        before = len(self.commands())
        self.apply()
        self.assertNotEqual(old, unit.read_bytes())
        self.assertEqual(["systemctl", "restart", "xivstream-cpu-policy.service"], self.commands()[-1])
        self.assertFalse(any(c[0] == "incus" for c in self.commands()[before:]))
        self.run_cli("cpu-config", "--busy", "2-3")
        report = self.run_cli("cpu-config")
        self.assertIn("Busy: 2-3\nIdle: 0-7", report)
        self.assertIn("CPU CORE SOCKET ONLINE MAXMHZ", self.run_cli("cpu-config", "--list"))

    def test_offline_selection_refuses_before_saving(self):
        before = self.config.read_bytes()
        self.run_cli("cpu-config", "--busy", "1000-1001", expected=1)
        self.assertEqual(before, self.config.read_bytes())
        self.assertEqual([], self.commands())

    def test_restore_retries_after_service_stopped(self):
        self.run_cli("cpu-config", "--disable")
        self.write_state(fail_writes=1)
        self.apply(expected=1)
        self.assertFalse(self.state()["active"])
        self.assertFalse(self.state()["enabled"])
        self.assertEqual("4-7", self.state()["limit"])
        before = len(self.commands())
        self.apply()
        self.assertEqual("0-7", self.state()["limit"])
        self.assertNotIn(["systemctl", "disable", "--now", "xivstream-cpu-policy.service"], self.commands()[before:])
        self.assertEqual(["incus", "--force-local", "config", "set", "game-fixture", "limits.cpu=0-7"], self.commands()[-1])

    def test_static_selection_changes_while_disabled(self):
        self.write_state(enabled=False, active=False)
        self.write_config(busy="", idle="", static="2-5")
        self.apply()
        self.assertEqual("2-5", self.state()["limit"])
        self.write_config(busy="", idle="", static="")
        self.apply()
        self.assertEqual("", self.state()["limit"])
        self.assertFalse(any(c[1] in ("disable", "restart", "enable") for c in self.commands() if c[0] == "systemctl"))

    def test_disabled_apply_is_idempotent(self):
        self.write_state(enabled=False, active=False, limit="0-7")
        self.write_config(busy="", idle="")
        self.apply()
        self.assertEqual(["incus", "--force-local", "config", "get", "game-fixture", "limits.cpu"], self.commands()[-1])
        self.assertEqual(3, len(self.commands()))

    def test_failed_stop_does_not_restore_while_daemon_running(self):
        self.write_state(fail_disable=True)
        self.write_config(busy="", idle="")
        self.apply(expected=1)
        self.assertTrue(self.state()["active"])
        self.assertFalse(any(c[0] == "incus" for c in self.commands()))

    def test_plan_does_not_write_or_change_service(self):
        before = self.state()
        output = self.run_cli("plan", "--cpu-policy-only", "-v")
        self.assertIn("Write the dynamic CPU policy service", output)
        self.assertEqual(before, self.state())
        self.assertFalse((self.etc / "systemd").exists())
        self.assertFalse((self.etc / "xivstream").exists())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--install-path", choices=("/usr/bin/xivstream", "/usr/local/bin/xivstream"), default="/usr/bin/xivstream")
    args = parser.parse_args()
    if sys.platform != "linux" or not shutil.which("bwrap"):
        parser.error("Linux and bubblewrap are required; no unsandboxed fallback")
    CPUOnlyPackageTests.binary = args.binary.resolve(strict=True)
    CPUOnlyPackageTests.install_path = args.install_path
    unittest.main(argv=[sys.argv[0]], verbosity=2)


if __name__ == "__main__":
    main()
