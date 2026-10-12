#!/usr/bin/env python3
"""Exercise switch refusal, rollback and boot selection in a synthetic host."""
import importlib.machinery
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest

SOURCE = Path(__file__).resolve().parents[1] / 'internal/assets/files/xivstream-backend'
sys.dont_write_bytecode = True


class BackendSwitchTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        loader = importlib.machinery.SourceFileLoader('backend_switch', str(SOURCE))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        self.b = importlib.util.module_from_spec(spec)
        loader.exec_module(self.b)
        for key, name in [('CONFIG', 'config.toml'), ('WOLF_CONFIG', 'wolf.toml'),
                          ('SUNSHINE_CONFIG', 'sunshine.toml'), ('QUADLET', 'wolf.container')]:
            setattr(self.b, key, self.root / name)
        self.b.PROC = self.root / 'proc'
        self.b.PROC.mkdir()
        home = self.root / 'game-home'
        (home / '.xlcore').mkdir(parents=True)
        (home / '.xlcore/launcher.ini').write_text('test')
        wolf = 'backend = "wolf"\ntopology = "host"\n[wolf]\n  autostart = true\n  home = ' + repr(str(home)) + '\n'
        self.b.WOLF_CONFIG.write_text(wolf)
        self.b.CONFIG.write_text(wolf)
        self.b.SUNSHINE_CONFIG.write_text('backend = "sunshine"\ntopology = "incus"\n')
        self.b.QUADLET.write_text('[Container]\nImage=example.test/wolf\n[Install]\nWantedBy=multi-user.target\n')
        self.states = {'wolf.service': True, 'xivstream-sunshine.service': False}
        self.calls = []
        self.b.active = lambda unit: self.states.get(unit, False)
        self.b.run = self.run_command
        self.b.status = lambda: None

    def run_command(self, *args):
        self.calls.append(args)
        if args[1] in ('start', 'stop'):
            self.states[args[2]] = args[1] == 'start'

    def test_active_game_refuses_switch_without_mutation(self):
        pid = self.b.PROC / '123'
        pid.mkdir()
        (pid / 'comm').write_text('ffxiv_dx11.exe\n')
        before = self.b.CONFIG.read_bytes(), self.b.QUADLET.read_bytes()
        with self.assertRaisesRegex(RuntimeError, 'Log out and quit'):
            self.b.switch('sunshine')
        self.assertEqual(before, (self.b.CONFIG.read_bytes(), self.b.QUADLET.read_bytes()))
        self.assertEqual([], self.calls)

    def test_switch_selects_backend_and_boot_with_custom_shared_home(self):
        self.b.switch('sunshine')
        self.assertEqual('sunshine', tomllib.loads(self.b.CONFIG.read_text())['backend'])
        self.assertFalse(tomllib.loads(self.b.WOLF_CONFIG.read_text())['wolf']['autostart'])
        self.assertNotIn('WantedBy=multi-user.target', self.b.QUADLET.read_text())
        self.assertTrue(self.states['xivstream-sunshine.service'])
        self.assertFalse(self.states['wolf.service'])
        self.assertIn(('systemctl', 'enable', 'xivstream-sunshine.service'), self.calls)
        self.b.switch('wolf')
        self.assertEqual('wolf', tomllib.loads(self.b.CONFIG.read_text())['backend'])
        self.assertTrue(tomllib.loads(self.b.WOLF_CONFIG.read_text())['wolf']['autostart'])
        self.assertIn('WantedBy=multi-user.target', self.b.QUADLET.read_text())
        self.assertFalse(self.states['xivstream-sunshine.service'])

    def test_staged_quadlet_without_install_can_switch_back(self):
        self.b.QUADLET.write_text('[Container]\nImage=example.test/wolf\n')
        self.b.set_boot('wolf')
        self.assertIn('[Install]\nWantedBy=multi-user.target', self.b.QUADLET.read_text())

    def test_failed_start_restores_previous_config_and_running_backend(self):
        before = self.b.CONFIG.read_bytes()
        def fail_start(*args):
            if args == ('systemctl', 'start', 'xivstream-sunshine.service'):
                raise subprocess.CalledProcessError(1, args)
            self.run_command(*args)
        self.b.run = fail_start
        with self.assertRaises(subprocess.CalledProcessError):
            self.b.switch('sunshine')
        self.assertEqual(before, self.b.CONFIG.read_bytes())
        self.assertTrue(self.states['wolf.service'])
        self.assertFalse(self.states['xivstream-sunshine.service'])
        self.assertTrue(tomllib.loads(self.b.WOLF_CONFIG.read_text())['wolf']['autostart'])

    def test_missing_home_refuses_before_stopping_anything(self):
        (self.root / 'game-home/.xlcore/launcher.ini').unlink()
        with self.assertRaisesRegex(RuntimeError, 'shared game home is missing'):
            self.b.switch('sunshine')
        self.assertEqual([], self.calls)

    def test_already_selected_backend_does_not_restart_active_game(self):
        pid = self.b.PROC / '123'
        pid.mkdir()
        (pid / 'comm').write_text('ffxiv_dx11.exe')
        self.b.switch('wolf')
        self.assertEqual([], self.calls)


if __name__ == '__main__':
    unittest.main()
