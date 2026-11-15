"""Safe boundary for an existing Chrome WebUI loadUnpacked operation.

The caller must provide a driver that proves chooser-to-window ownership,
accessibility and focus before each input, and a binding verifier for the
selected target, browser context, process incarnation and native window.
After dispatch, failures are UNKNOWN and require independent readback before
retry. No profile launch, consent handling or page content access occurs here.
"""
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
import math
import time
import threading


EXPRESSION = ("new Promise(resolve => chrome.developerPrivate.loadUnpacked("
              "{failQuietly:true}, () => resolve(chrome.runtime.lastError?.message || 'ok')))")


@dataclass(frozen=True)
class Binding:
    pid: int
    process_identity: str
    chrome_window: str
    target_id: str
    context_id: str
    session_id: str

    def validate(self):
        if type(self.pid) is not int or self.pid <= 0 or any(
            not isinstance(x, str) or not x.strip() for x in
            (self.process_identity, self.chrome_window, self.target_id,
             self.context_id, self.session_id)
        ):
            raise ValueError('incomplete process, window or target binding')


@dataclass(frozen=True)
class Result:
    status: str
    detail: str


def load_unpacked(binding, directory, timeout, *, check_binding, chooser, send):
    """Invoke via existing WebUI session; never create or discover a target.

    check_binding(binding) must prove that the exact session still addresses
    chrome://extensions/ in the selected context and that the PID incarnation
    and native Chrome window remain the same. chooser.prepare(binding) must
    snapshot windows and fail on inaccessible/ambiguous state. chooser.select
    must establish that the new chooser belongs to that exact window and check
    ownership, focus, binding and policy/consent before every input action.
    send(method, params, session_id, timeout) sends CDP and honors timeout.
    All integrations must fail closed; no implementation is supplied here.
    ACKNOWLEDGED is Chrome's callback, not proof of installation.
    """
    try:
        binding.validate()
        if (isinstance(timeout, bool) or not isinstance(timeout, (int, float))
                or not math.isfinite(timeout) or not 0 < timeout <= 120):
            raise ValueError('invalid timeout')
        if not isinstance(directory, str) or any(ord(c) < 32 for c in directory):
            raise ValueError('invalid directory')
        path = Path(directory)
        if not path.is_absolute() or not path.is_dir():
            raise ValueError('directory must be an existing absolute path')
        if not check_binding(binding):
            raise ValueError('binding could not be proven')
        chooser.prepare(binding)
        if not check_binding(binding):
            raise ValueError('binding changed before dispatch')
    except Exception as exc:
        return Result('NOT_SENT', str(exc))
    deadline = time.monotonic() + timeout
    executor = ThreadPoolExecutor(max_workers=1)
    dispatched = threading.Event()
    def dispatch():
        try:
            marker = getattr(chooser, 'mark_dispatched', None)
            if marker is not None:
                marker(binding)
            dispatched.set()
            return send('Runtime.evaluate', {
        'expression': EXPRESSION, 'userGesture': True, 'awaitPromise': True,
        'returnByValue': True}, binding.session_id, timeout)
        finally:
            dispatched.set()
    future = executor.submit(dispatch)
    try:
        if not dispatched.wait(max(0, deadline - time.monotonic())):
            raise TimeoutError('dispatch marker timed out')
        if future.done():
            response = future.result()
            remote = response.get('result', {}) if isinstance(response, dict) else {}
            if remote.get('type') == 'string' and isinstance(remote.get('value'), str) and remote['value'] != 'ok':
                return Result('REJECTED', remote['value'])
        def binding_and_pending():
            if future.done():
                raise RuntimeError('Chrome responded before chooser completed')
            return bool(check_binding(binding))
        chooser.select(binding, str(path), deadline, binding_and_pending)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError('deadline elapsed')
        response = future.result(timeout=remaining)
        if not check_binding(binding):
            raise RuntimeError('binding changed after dispatch')
        if not isinstance(response, dict) or response.get('exceptionDetails'):
            raise RuntimeError('invalid or exceptional CDP response')
        remote = response.get('result')
        if not isinstance(remote, dict) or remote.get('type') != 'string' or not isinstance(remote.get('value'), str):
            raise RuntimeError('invalid Chrome callback')
        if remote['value'] == 'ok':
            return Result('ACKNOWLEDGED', 'Chrome callback succeeded; independent readback required')
        return Result('UNKNOWN', 'Chrome reported an error after chooser input; independent readback required: ' + remote['value'])
    except Exception as exc:
        if future.done():
            try:
                response = future.result()
                remote = response.get('result', {}) if isinstance(response, dict) else {}
                if (getattr(chooser, 'input_sent', None) is False and remote.get('type') == 'string'
                        and isinstance(remote.get('value'), str) and remote['value'] != 'ok'):
                    return Result('REJECTED', remote['value'])
            except Exception:
                pass
        return Result('UNKNOWN', str(exc) or type(exc).__name__)
    finally:
        # Running sends cannot be safely cancelled. The caller owns the CDP
        # connection and must reconcile before any retry.
        executor.shutdown(wait=False, cancel_futures=True)
