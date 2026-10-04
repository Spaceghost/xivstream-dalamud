#!/usr/bin/env python3
"""game-cpu-fence against fake cgroup and /proc trees, for both layouts.

Run by internal/assets/fence_test.go (FENCE=<path to the script>); nothing on
the host is read or changed: systemctl is a fake that logs its calls.
"""

import sys
sys.dont_write_bytecode = True
import importlib.machinery
import importlib.util
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

FENCE = os.environ.get("FENCE") or str(Path(__file__).resolve().parents[1] / "files" / "game-cpu-fence")

FAKE_SYSTEMCTL = r'''#!/usr/bin/env python3
import json, os, sys
log = os.environ["FAKE_LOG"]
args = sys.argv[1:]
with open(log, "a") as f:
    f.write(json.dumps(args) + "\n")
if args[:1] == ["show"]:
    units = args[args.index("--") + 1:]
    blocks = []
    for u in units:
        allowed = json.load(open(os.environ["FAKE_ALLOWED"])).get(u, "")
        blocks.append(f"Id={u}\nAllowedCPUs={allowed}")
    print("\n\n".join(blocks))
sys.exit(0)
'''


class Tree:
    def __init__(self, root: Path):
        self.root = root
        self.cg = root / "cgroup"
        self.proc = root / "proc"
        self.cg.mkdir()
        self.proc.mkdir()
        (self.cg / "cpuset.cpus.effective").write_text("0-7\n")
        self.stat(1000, 500)

    def group(self, rel: str, cpus: str = "", procs: str = "") -> Path:
        d = self.cg / rel
        d.mkdir(parents=True, exist_ok=True)
        (d / "cpuset.cpus").write_text(cpus + "\n")
        (d / "cgroup.procs").write_text(procs + "\n")
        (d / "cpu.stat").write_text("usage_usec 0\n")
        return d

    def process(self, pid: int, comm: str, cgroup: str) -> None:
        d = self.proc / str(pid)
        d.mkdir(parents=True, exist_ok=True)
        (d / "comm").write_text(comm + "\n")
        (d / "cgroup").write_text(f"0::/{cgroup}\n")
        self.group(cgroup, procs=str(pid))

    def stat(self, total: int, idle: int) -> None:
        # user nice system idle iowait irq softirq steal: total jiffies, idle in field 4
        busy = total - idle
        (self.proc / "stat").write_text(f"cpu  {busy} 0 0 {idle} 0 0 0 0 0 0\ncpu0 0 0 0 0\n")


def load_fence(tree: Tree, config: str):
    env = {
        "CGROUP_ROOT": str(tree.cg), "PROC_ROOT": str(tree.proc),
        "XIVSTREAM_CONFIG": str(tree.root / "config.toml"),
        "RUNTIME_DIRECTORY": str(tree.root / "run"),
        "SYSTEMCTL": str(tree.root / "systemctl"),
        "FAKE_LOG": str(tree.root / "systemctl.log"),
        "FAKE_ALLOWED": str(tree.root / "allowed.json"),
        "SAMPLE": "5",
    }
    for k in ("FENCE_CPUS", "GAME_CPUS", "EXEMPT", "GAME_CT"):
        os.environ.pop(k, None)
    os.environ.update(env)
    (tree.root / "config.toml").write_text(config)
    (tree.root / "allowed.json").write_text("{}")
    sc = tree.root / "systemctl"
    sc.write_text(FAKE_SYSTEMCTL)
    sc.chmod(0o755)
    loader = importlib.machinery.SourceFileLoader("game_cpu_fence", FENCE)
    spec = importlib.util.spec_from_loader("game_cpu_fence", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


def calls(tree: Tree) -> list[list[str]]:
    log = tree.root / "systemctl.log"
    if not log.exists():
        return []
    return [json.loads(line) for line in log.read_text().splitlines()]


def fenced_units(tree: Tree, cpus: str) -> set[str]:
    return {c[3] for c in calls(tree) if c[:3] == ["set-property", "--runtime", "--"] and c[4] == f"AllowedCPUs={cpus}"}


INCUS_CONFIG = """
topology = "incus"
backend = "sunshine"
[incus]
container = "ffxiv"
[incus.cpu_policy]
busy_cpus = "4-7"
idle_cpus = "0-7"
"""

WOLF_CONFIG = """
topology = "host"
backend = "wolf"
[incus.cpu_policy]
busy_cpus = "4-7"
idle_cpus = "0-7"
busy_threshold_percent = 25
idle_threshold_percent = 10
busy_after_seconds = 15
idle_after_seconds = 30
"""


class IncusLayout(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.t = Tree(Path(self.tmp.name))
        t = self.t
        t.process(100, "ffxiv_dx11.exe", "lxc.payload.ffxiv/user.slice/game.scope")
        t.group("lxc.monitor.ffxiv", "")
        t.group("lxc.payload.almanac", "0-7")
        t.group("lxc.payload.pinned", "1-2")  # Incus already keeps it off 4-7
        t.group("user.slice")
        t.group("machine.slice")
        t.group("system.slice/foo.service")
        t.group("system.slice/tailscaled.service")
        t.group("system.slice/wolf.service")
        self.f = load_fence(t, INCUS_CONFIG)

    def tearDown(self):
        self.tmp.cleanup()

    def test_fences_everything_but_the_game_container(self):
        fence = self.f.Fence(*self.f.game_identity())
        self.assertTrue(fence.game_running())
        self.assertEqual(fence.layout, "incus")
        self.assertTrue(fence.engage())
        fence.assert_fence(0.0)
        self.assertEqual((self.t.cg / "lxc.payload.almanac/cpuset.cpus").read_text().strip(), "0-3")
        self.assertEqual((self.t.cg / "lxc.payload.pinned/cpuset.cpus").read_text().strip(), "1-2")
        self.assertEqual((self.t.cg / "lxc.payload.ffxiv/user.slice/game.scope/cpuset.cpus").read_text().strip(), "")
        # the whole of machine.slice, as before Wolf; tailscaled and wolf exempt
        self.assertEqual(fenced_units(self.t, "0-3"), {"user.slice", "machine.slice", "foo.service"})
        fence.balance(0.0)  # Incus: cpu-policy owns the game's CPUs
        self.assertFalse(any("lxc.payload.ffxiv" in " ".join(c) for c in calls(self.t)))

    def test_state_is_compatible_and_release_gives_back(self):
        fence = self.f.Fence(*self.f.game_identity())
        fence.game_running()
        fence.engage()
        fence.assert_fence(0.0)
        saved = (self.t.root / "run" / "saved").read_text().splitlines()
        self.assertIn("ct\tlxc.payload.almanac\t0-7", saved)
        self.assertIn("unit\tuser.slice\t", saved)
        again = self.f.Fence(*self.f.game_identity())  # a restarted daemon
        again.release()
        self.assertEqual((self.t.cg / "lxc.payload.almanac/cpuset.cpus").read_text().strip(), "0-7")
        self.assertIn(["set-property", "--runtime", "--", "foo.service", "AllowedCPUs="], calls(self.t))


class PodmanLayout(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.t = Tree(Path(self.tmp.name))
        t = self.t
        self.scope = "libpod-" + "a" * 64 + ".scope"
        t.process(200, "ffxiv_dx11.exe", f"machine.slice/{self.scope}/container")
        t.group("machine.slice/libpod-conmon-" + "a" * 64 + ".scope")
        t.group("machine.slice/libpod-" + "b" * 64 + ".scope")
        t.group("lxc.payload.almanac", "0-7")
        t.group("user.slice")
        t.group("system.slice/foo.service")
        t.group("system.slice/wolf.service")
        t.group("system.slice/tailscaled.service")
        self.f = load_fence(t, WOLF_CONFIG)

    def tearDown(self):
        self.tmp.cleanup()

    def test_fences_siblings_never_the_game_scope(self):
        fence = self.f.Fence(*self.f.game_identity())
        self.assertTrue(fence.game_running())
        self.assertEqual((fence.layout, fence.scope), ("podman", self.scope))
        self.assertTrue(fence.engage())
        fence.assert_fence(0.0)
        fenced = fenced_units(self.t, "0-3")
        self.assertIn("user.slice", fenced)
        self.assertIn("foo.service", fenced)
        self.assertIn("libpod-" + "b" * 64 + ".scope", fenced)
        self.assertIn("libpod-conmon-" + "a" * 64 + ".scope", fenced)
        self.assertNotIn("machine.slice", fenced)  # would fence the game with it
        self.assertNotIn(self.scope, fenced)
        self.assertNotIn("wolf.service", fenced)
        self.assertNotIn("tailscaled.service", fenced)
        self.assertEqual((self.t.cg / "lxc.payload.almanac/cpuset.cpus").read_text().strip(), "0-3")

    def test_game_scope_switches_busy_and_idle(self):
        fence = self.f.Fence(*self.f.game_identity())
        fence.game_running()
        fence.engage()
        fence.balance(0.0)
        self.assertEqual(fenced_units(self.t, "4-7"), {self.scope})  # starts busy
        game = self.t.cg / "machine.slice" / self.scope / "cpu.stat"
        total = idle = 0
        usage = 0
        now = 0.0
        # The host otherwise idle: 8 CPUs, 500 jiffies per 5 s per... the game uses
        # 2 CPUs' worth, the rest is idle. After idle_after (30 s): idle CPUs.
        for _ in range(9):
            now += 5
            total += 4000
            idle += 3000   # 25% busy in all, all of it the game's
            usage += 10_000_000  # 2 CPUs for 5 s = 10 s = 25% of 8 CPUs
            self.t.stat(total, idle)
            game.write_text(f"usage_usec {usage}\n")
            fence.balance(now)
        self.assertEqual(fenced_units(self.t, "0-7"), {self.scope})
        # Then other work takes half the machine for busy_after (15 s): busy again.
        before = len(calls(self.t))
        for _ in range(4):
            now += 5
            total += 4000
            idle += 1000   # 75% busy, a third of it the game's
            usage += 10_000_000
            self.t.stat(total, idle)
            game.write_text(f"usage_usec {usage}\n")
            fence.balance(now)
        later = calls(self.t)[before:]
        self.assertIn(["set-property", "--runtime", "--", self.scope, "AllowedCPUs=4-7"], later)

    def test_release_gives_the_scope_back(self):
        fence = self.f.Fence(*self.f.game_identity())
        fence.game_running()
        fence.engage()
        fence.assert_fence(0.0)
        fence.balance(0.0)
        fence.release()
        self.assertIn(["set-property", "--runtime", "--", self.scope, "AllowedCPUs="], calls(self.t))
        self.assertEqual((self.t.cg / "lxc.payload.almanac/cpuset.cpus").read_text().strip(), "0-7")

    def test_wolf_section_overrides_cpu_policy(self):
        (self.t.root / "config.toml").write_text(WOLF_CONFIG + '\n[wolf]\ncpus_busy = "5-7"\ncpus_idle = "1-7"\n')
        fence = self.f.Fence(*self.f.game_identity())
        fence.game_running()
        fence.engage()
        self.assertEqual(self.f.format_cpus(fence.cpus), "0-4")
        fence.balance(0.0)
        self.assertEqual(fenced_units(self.t, "5-7"), {self.scope})


class LoadWindow(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        t = Tree(Path(self.tmp.name))
        self.f = load_fence(t, WOLF_CONFIG)
        self.policy = self.f.Policy(self.f.config())

    def tearDown(self):
        self.tmp.cleanup()

    def test_unknown_measurements_are_busy(self):
        load = self.f.Load()
        self.assertEqual(load.observe(0, (100, 50), 0, 8, self.policy), "")
        self.assertEqual(load.observe(5, None, 0, 8, self.policy), "busy")
        self.assertEqual(load.observe(10, (200, 100), 0, 8, self.policy), "")  # restarts the window
        self.assertEqual(load.observe(15, (300, 150), 0, 8, self.policy), "")

    def test_a_stall_is_not_idle_time(self):
        load = self.f.Load()
        load.observe(0, (0, 0), 0, 8, self.policy)
        self.assertEqual(load.observe(100, (100000, 99000), 0, 8, self.policy), "busy")

    def test_counts_need_lists(self):
        self.assertTrue(self.policy.switches())
        (Path(self.tmp.name) / "config.toml").write_text('[incus.cpu_policy]\nbusy_cpus = "4"\nidle_cpus = "8"\n')
        self.assertFalse(self.f.Policy(self.f.config()).switches())


if __name__ == "__main__":
    unittest.main(verbosity=2)
