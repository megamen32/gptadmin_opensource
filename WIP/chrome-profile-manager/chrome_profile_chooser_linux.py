"""Fail-closed X11 driver for a caller-authorized native directory chooser.

Call prepare(binding), then mark_dispatched(binding) synchronously immediately
before sending loadUnpacked, then select(...). The caller must wire the marker
into the actual sender. process_identity must equal process_identity(pid).
authorize(binding, chooser_xid, action) must independently prove that this is
the intended directory chooser and authorize each action, including consent
state. It must reject permission and consent dialogs. check_binding must prove
the exact target, context, session, browser process and native window binding.
Successful return only means input was sent, not installation succeeded.
"""
import math
import os
import re
import subprocess
import time
from pathlib import Path


def process_identity(pid):
    if type(pid) is not int or pid <= 0:
        raise ValueError('invalid PID')
    stat = Path(f'/proc/{pid}/stat').read_text()
    fields = stat[stat.rfind(')') + 2:].split()
    boot = Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    return f'linux:{boot}:{fields[19]}'


class LinuxChooser:
    def __init__(self, *, authorize, display=None, poll_interval=0.05, perform_action=None):
        if not callable(authorize) or not 0 < poll_interval <= 0.5:
            raise ValueError('authorization callback and bounded interval required')
        self.authorize = authorize
        self.perform_action = perform_action
        self.env = os.environ.copy()
        if display is not None:
            self.env['DISPLAY'] = display
        if not self.env.get('DISPLAY'):
            raise ValueError('X11 DISPLAY required')
        self.interval = poll_interval
        self._prepared = None
        self._dispatched = False
        self.input_sent = False

    def _run(self, *args, deadline=None, allow_no_match=False):
        timeout = 2.0 if deadline is None else min(2.0, deadline - time.monotonic())
        if timeout <= 0:
            raise TimeoutError('chooser deadline elapsed')
        result = subprocess.run(args, env=self.env, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=timeout, check=False)
        if result.returncode and not (allow_no_match and result.returncode == 1 and not result.stderr.strip()):
            raise RuntimeError(f'{args[0]} failed: {result.stderr.strip()}')
        return result.stdout

    def _windows(self, deadline=None):
        out = self._run('xdotool', 'search', '--onlyvisible', '--name', '.*',
                        deadline=deadline, allow_no_match=True)
        return {int(s) for s in out.split()}

    def _property(self, xid, name, deadline=None):
        return self._run('xprop', '-id', str(xid), name, deadline=deadline)

    def _target(self, binding, deadline=None):
        binding.validate()
        try:
            target = int(binding.chrome_window, 0)
        except (TypeError, ValueError):
            raise ValueError('chrome_window must be a numeric X11 window ID')
        if target <= 0 or process_identity(binding.pid) != binding.process_identity:
            raise RuntimeError('browser process identity changed')
        prop = self._property(target, '_NET_WM_PID', deadline)
        match = re.fullmatch(r'_NET_WM_PID\(CARDINAL\) = (\d+)\s*', prop)
        if not match or int(match.group(1)) != binding.pid:
            raise RuntimeError('browser window PID mismatch')
        if target not in self._windows(deadline):
            raise RuntimeError('browser window is not visible')
        return target

    def prepare(self, binding):
        self._prepared = None
        self._dispatched = False
        self.input_sent = False
        target = self._target(binding)
        self._prepared = (binding, target, self._windows())

    def mark_dispatched(self, binding):
        if self._prepared is None or self._prepared[0] != binding or self._dispatched:
            raise RuntimeError('missing or reused preparation')
        self._target(binding)
        self._prepared = (binding, self._prepared[1], self._windows())
        self._dispatched = True

    def _owned(self, window, target, deadline):
        prop = self._property(window, 'WM_TRANSIENT_FOR', deadline)
        match = re.fullmatch(r'WM_TRANSIENT_FOR\(WINDOW\): window id # (0x[0-9a-fA-F]+)\s*', prop)
        return bool(match and int(match.group(1), 16) == target)

    def select(self, binding, directory, deadline, check_binding):
        if (self._prepared is None or self._prepared[0] != binding or not self._dispatched
                or not callable(check_binding)):
            raise RuntimeError('dispatch and exact binding required')
        if (not isinstance(deadline, (int, float)) or isinstance(deadline, bool)
                or not math.isfinite(deadline)):
            raise ValueError('invalid deadline')
        if (not isinstance(directory, str) or not Path(directory).is_absolute()
                or not Path(directory).is_dir() or any(ord(c) < 32 for c in directory)):
            raise ValueError('existing absolute directory required')
        _, target, old = self._prepared
        self._prepared = None
        while True:
            if time.monotonic() >= deadline:
                raise TimeoutError('new chooser not found')
            if self._target(binding, deadline) != target or check_binding() is not True:
                raise RuntimeError('binding not proven')
            candidates = [w for w in self._windows(deadline) - old if self._owned(w, target, deadline)]
            if len(candidates) > 1:
                raise RuntimeError('ambiguous transient windows')
            if candidates:
                window = candidates[0]
                break
            time.sleep(min(self.interval, max(0, deadline - time.monotonic())))
        for action, keys in [('location', ('key', 'ctrl+l')),
                             ('path', ('type', '--clearmodifiers', '--', directory)),
                             ('navigate', ('key', 'Return')),
                             ('select', ('key', 'Return'))]:
            if self._target(binding, deadline) != target or check_binding() is not True:
                raise RuntimeError('binding not proven before input')
            if window not in self._windows(deadline) or not self._owned(window, target, deadline):
                raise RuntimeError('chooser ownership changed')
            if self.authorize(binding, window, action) is not True:
                raise PermissionError('chooser action not authorized')
            focused = self._run('xdotool', 'getwindowfocus', deadline=deadline).strip()
            if not focused.isdigit() or int(focused) != window:
                raise RuntimeError('chooser does not have exact focus')
            self.input_sent = True
            if self.perform_action is None or not self.perform_action(binding, window, action):
                self._run('xdotool', *keys, deadline=deadline)
            # GTK resolves a typed location asynchronously. Do not send the next
            # action before the dialog has processed the previous one.
            time.sleep(min(0.35, max(0, deadline - time.monotonic())))
