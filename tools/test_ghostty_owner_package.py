#!/usr/bin/env python3
"""Actual CLI owner-file gate in a rootless, networkless synthetic home.

python3 tools/test_ghostty_owner_package.py --binary /private/path/xivstream
Requires bubblewrap. Never invokes bootstrap/services, a live agent or sudo.
"""
import argparse
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import tarfile
import unittest


class OwnerPackageTests(unittest.TestCase):
    binary: Path
    real_bundle = None
    real_agent = None
    real_version = None

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="xivstream-owner-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "home"
        self.home.mkdir(mode=0o700)
        self.etc = self.root / "etc"
        self.etc.mkdir()
        (self.etc / "passwd").write_text(
            f"fixture:x:{os.getuid()}:{os.getgid()}:Synthetic owner:/home/fixture:/bin/false\n")
        (self.etc / "group").write_text(f"fixture:x:{os.getgid()}:\n")
        (self.etc / "nsswitch.conf").write_text("passwd: files\ngroup: files\n")

    def cli(self, *args, payload=b"", success=True, root=False):
        cmd = [shutil.which("bwrap"), "--unshare-all", "--die-with-parent", "--new-session",
               "--clearenv", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib",
               "--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev",
               "--tmpfs", "/tmp", "--tmpfs", "/run", "--ro-bind", str(self.etc), "/etc",
               "--bind", str(self.home), "/home/fixture", "--ro-bind", str(self.binary), "/xivstream"]
        if root:
            cmd += ["--uid", "0", "--gid", "0"]
        cmd += ["/xivstream", "internal-owner-file-v1", *args]
        result = subprocess.run(cmd, input=payload, capture_output=True, timeout=20)
        self.assertNotIn(b"bwrap:", result.stderr)
        self.assertEqual(0 if success else 1, result.returncode, result.stderr.decode())
        if not success:
            self.assertEqual(b"", result.stdout)
            if payload:
                self.assertNotIn(payload.strip(), result.stderr)
        return result.stdout

    def token_path(self):
        return self.home / ".xlcore/pluginConfigs/GhosttyDalamud/host-agent.token"

    def test_exact_owner_identity_ignores_ambient_home(self):
        identity = json.loads(self.cli("identity"))
        self.assertEqual("fixture", identity["User"])
        self.assertEqual(str(os.getuid()), identity["UID"])
        self.assertEqual("/home/fixture", identity["Home"])

    def test_root_refused_before_filesystem_change(self):
        self.cli("write", "host-token-copy", payload=b"synthetic-private-token-1234", success=False, root=True)
        self.assertFalse(self.token_path().exists())

    def test_fresh_private_roundtrip_and_atomic_replace(self):
        self.assertFalse(json.loads(self.cli("inspect", "host-token-copy"))["exists"])
        for payload in (b"synthetic-private-token-1234\n", b"synthetic-replacement-token-5678\n"):
            self.assertEqual(b"ok\n", self.cli("write", "host-token-copy", payload=payload))
            self.assertEqual(payload, self.cli("read", "host-token-copy"))
            self.assertEqual(0o600, stat.S_IMODE(self.token_path().stat().st_mode))
            self.assertEqual(os.getuid(), self.token_path().stat().st_uid)
            self.assertEqual([self.token_path()], list(self.token_path().parent.iterdir()))

    def test_sources_read_only_and_private(self):
        source = self.home / ".config/xivstream/ghostty-host.token"
        source.parent.mkdir(parents=True, mode=0o700)
        source.write_bytes(b"synthetic-private-token-1234\n")
        source.chmod(0o600)
        self.assertEqual(source.read_bytes(), self.cli("read", "host-token-source"))
        self.cli("write", "host-token-source", payload=b"synthetic-replacement-token-5678", success=False)
        source.chmod(0o644)
        self.cli("read", "host-token-source", success=False)

    def test_destination_symlink_and_parent_symlink_refused(self):
        for parent in (False, True):
            with self.subTest(parent=parent):
                self.setUp()
                victim = self.home / "victim"
                victim.write_bytes(b"protected synthetic preimage")
                target = self.token_path()
                if parent:
                    outside = self.home / "elsewhere"
                    outside.mkdir()
                    (self.home / ".xlcore").symlink_to("elsewhere")
                else:
                    target.parent.mkdir(parents=True)
                    target.symlink_to("/home/fixture/victim")
                self.cli("write", "host-token-copy", payload=b"synthetic-private-token-1234", success=False)
                self.assertEqual(b"protected synthetic preimage", victim.read_bytes())

    def test_oversize_arbitrary_object_and_fifo_refused(self):
        self.cli("write", "host-token-copy", payload=b"x" * 1026, success=False)
        self.cli("write", "/etc/passwd", payload=b"synthetic-private-token-1234", success=False)
        self.assertFalse(self.token_path().exists())
        self.token_path().parent.mkdir(parents=True)
        os.mkfifo(self.token_path(), 0o600)
        self.cli("read", "host-token-copy", success=False)

    def test_binary_and_unit_create_only_with_fixed_modes(self):
        for name, relative, payload, mode in (
            ("agent-binary", ".local/lib/xivstream/ghostty-agent", b"\x7fELFsynthetic-not-executed", 0o755),
            ("host-unit", ".config/systemd/user/xivstream-ghostty-host.service",
             b"# Managed by xivstream; never restarted automatically on re-apply.\nfixture\n", 0o600),
        ):
            self.cli("write", name, payload=payload)
            target = self.home / relative
            self.assertEqual(mode, stat.S_IMODE(target.stat().st_mode))
            self.cli("write", name, payload=payload)
            self.cli("write", name, payload=payload + b"other", success=False)
            self.assertEqual(payload, target.read_bytes())

    def test_unmanaged_defaults_preserved(self):
        target = self.home / ".xlcore/pluginConfigs/GhosttyDalamud/agent-defaults.lua"
        target.parent.mkdir(parents=True)
        target.write_bytes(b"return { user = true }\n")
        target.chmod(0o600)
        self.cli("write", "defaults", payload=b"-- Managed by xivstream: Ghostty first-run defaults v1.\nreturn {}\n", success=False)
        self.assertEqual(b"return { user = true }\n", target.read_bytes())

    def test_dalamud_config_legacy_read_private_replace(self):
        target = self.home / ".xlcore/dalamudConfig.json"
        target.parent.mkdir()
        target.write_bytes(b'{"UserSetting": true}')
        target.chmod(0o644)
        self.assertEqual(target.read_bytes(), self.cli("read", "dalamud-config"))
        replacement = b'{"UserSetting": true, "AnotherSetting": 1}'
        self.cli("write", "dalamud-config", payload=replacement)
        self.assertEqual(replacement, target.read_bytes())
        self.assertEqual(0o600, stat.S_IMODE(target.stat().st_mode))
        self.cli("write", "dalamud-config", payload=b"null", success=False)
        self.assertEqual(replacement, target.read_bytes())

    def bundle(self, extra=b""):
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            for name, body in {
                "GhosttyDalamud.dll": b"MZsynthetic assembly",
                "GhosttyDalamud.json": b'{"InternalName":"GhosttyDalamud","AssemblyVersion":"0.3.3.0"}',
                "lua/init.lua": b"return {}\n" + extra,
            }.items():
                member = tarfile.TarInfo(name)
                member.mode = 0o644
                member.size = len(body)
                archive.addfile(member, io.BytesIO(body))
        return output.getvalue()

    def test_actual_bundle_private_publish_retry_and_refuse_different(self):
        self.assertFalse(json.loads(self.cli("inspect-ghostty-bundle", "0.3.3.0"))["exists"])
        archive = self.bundle()
        for _ in range(2):
            self.cli("install-ghostty-bundle", "0.3.3.0", payload=archive)
        self.assertTrue(json.loads(self.cli("inspect-ghostty-bundle", "0.3.3.0"))["exists"])
        self.cli("install-ghostty-bundle", "0.3.3.0", payload=self.bundle(b"changed"), success=False)
        target = self.home / ".xlcore/installedPlugins/GhosttyDalamud/0.3.3.0/lua/init.lua"
        self.assertEqual(b"return {}\n", target.read_bytes())
        self.assertEqual(0o600, stat.S_IMODE(target.stat().st_mode))
        self.cli("install-ghostty-bundle", "../0.3.3.0", payload=archive, success=False)

    def test_optional_real_built_artifacts_roundtrip(self):
        if self.real_bundle is None:
            self.skipTest("optional real built Ghostty artifacts not supplied")
        archive = self.real_bundle.read_bytes()
        agent = self.real_agent.read_bytes()
        self.cli("install-ghostty-bundle", self.real_version, payload=archive)
        self.cli("install-ghostty-bundle", self.real_version, payload=archive)
        self.assertTrue(json.loads(self.cli("inspect-ghostty-bundle", self.real_version))["exists"])
        self.cli("write", "agent-binary", payload=agent)
        self.assertEqual(agent, self.cli("read", "agent-binary"))
        target = self.home / ".local/lib/xivstream/ghostty-agent"
        self.assertEqual(0o755, stat.S_IMODE(target.stat().st_mode))
        self.assertEqual(os.getuid(), target.stat().st_uid)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--real-bundle", type=Path)
    parser.add_argument("--real-agent", type=Path)
    parser.add_argument("--real-version")
    options = parser.parse_args()
    if os.getuid() == 0 or shutil.which("bwrap") is None:
        parser.error("requires a non-root user and bubblewrap; no unsandboxed fallback")
    OwnerPackageTests.binary = options.binary.resolve(strict=True)
    if any((options.real_bundle, options.real_agent, options.real_version)):
        if not all((options.real_bundle, options.real_agent, options.real_version)):
            parser.error("real bundle, agent and version must be supplied together")
        OwnerPackageTests.real_bundle = options.real_bundle.resolve(strict=True)
        OwnerPackageTests.real_agent = options.real_agent.resolve(strict=True)
        OwnerPackageTests.real_version = options.real_version
    unittest.main(argv=[__file__], verbosity=2)
