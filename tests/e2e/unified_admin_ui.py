"""Exercise the built admin against an isolated real Go Hub, never production."""
import json
import socket
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path
from playwright.sync_api import sync_playwright


def main() -> None:
    root = Path(__file__).resolve().parents[2]
    artifacts = root / '.tmp/unified-admin-ui'
    artifacts.mkdir(parents=True, exist_ok=True)
    subprocess.run(['go', 'build', '-o', str(artifacts / 'ui-canary'), './internal/hub/testdata/admin-canary'], cwd=root / 'go-hub', check=True, timeout=120)
    fixture = Path(tempfile.mkdtemp(prefix='browser-fixture-', dir=artifacts))
    (fixture / 'public').mkdir()
    (fixture / 'public/admin').symlink_to(root / 'admin-ui/dist', target_is_directory=True)
    (fixture / 'fixture.env').write_text('SHELLMCP_HEARTBEAT=0\n')
    # Reproduce the reported inventory, not an almost-empty happy-path fixture.
    config = fixture / 'config'
    config.mkdir()
    client_ids = ['gptadmin-' + format(i + 1, '032x') for i in range(8)]
    now = int(time.time())
    registrations = {client_id: {'created_at': now - 3600, 'redirect_uris': ['https://chatgpt.com/connector_platform/oauth/callback'], 'role': 'client'} for client_id in client_ids}
    credentials = {}
    for index in range(99):
        client_id = client_ids[0 if index < 11 else 1 + ((index - 11) % 7)]
        credential_id = f'fixture-refresh-{index:032d}'
        credentials[credential_id] = {'id': credential_id, 'client_id': client_id, 'token_kind': 'oauth_refresh', 'issued_at': now - index, 'revoked_at': now - index + 1, 'role': 'admin', 'scope': 'gptadmin.read gptadmin.exec'}
    (config / 'oauth_clients_state.json').write_text(json.dumps({'clients': registrations}))
    (config / 'mcp_tokens_state.json').write_text(json.dumps({'tokens': credentials}))
    (config / 'tasks_state.json').write_text(json.dumps({'relay_jobs': {
        'fixture-result': {'id': 'fixture-result', 'agent_id': 'fixture', 'method': 'tools/call', 'params': {'name': 'inspect', 'arguments': {'path': '/work'}}, 'created_at': now - 3, 'completed_at': now - 2, 'status': 'completed', 'result': {'stdout': 'SCREENSHOT_RESULT_VISIBLE'}},
        'fixture-error': {'id': 'fixture-error', 'agent_id': 'fixture', 'method': 'tools/call', 'params': {'name': 'inspect', 'arguments': {'path': '/missing'}}, 'created_at': now - 2, 'completed_at': now - 1, 'status': 'failed', 'error': {'message': 'SCREENSHOT_ERROR_VISIBLE'}},
    }}))

    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        address = f'127.0.0.1:{sock.getsockname()[1]}'
    url = 'http://' + address
    log = (artifacts / 'browser-canary.log').open('w')
    server = subprocess.Popen([str(artifacts / 'ui-canary'), str(fixture), address], stdout=log, stderr=log)
    errors = []
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(url + '/admin/', timeout=1).close()
                break
            except Exception:
                if server.poll() is not None:
                    raise RuntimeError('Canary exited before becoming ready')
                time.sleep(0.1)
        else:
            raise RuntimeError('Canary startup timeout')
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            page = browser.new_page(viewport={'width': 1440, 'height': 1000})
            page.set_default_timeout(5000)
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.goto(url + '/admin/')
            page.locator('input[name="password"]').fill('local-ui-canary')
            page.locator('input[name="password"]').press('Enter')
            page.locator('#view-overview.active').wait_for()
            page.locator('#status').filter(has_text='online').wait_for()
            assert page.locator('iframe').count() == 0
            page.screenshot(path=str(artifacts / 'unified-desktop.png'), full_page=True)
            with page.expect_response(lambda response: '/admin/api/overview' in response.url):
                page.locator('.operations-console .topbar button[onclick="refreshAll()"]').click()
            assert page.locator('#recentJobsCompact').inner_text().find('SCREENSHOT_RESULT_VISIBLE') >= 0
            assert page.locator('#recentJobsCompact').inner_text().find('SCREENSHOT_ERROR_VISIBLE') >= 0
            page.locator('#recentJobsCompact button').first.click()
            page.get_by_role('heading', name='Ошибка', exact=True).wait_for()
            assert 'SCREENSHOT_ERROR_VISIBLE' in page.locator('#jobDetailBody').inner_text()
            assert page.locator('#jobDetailBody .entryStatus').text_content() == 'failed'
            page.goto(url + '/admin/#clients?connection=' + client_ids[0])
            page.locator('[data-connection-id].selected').wait_for()
            assert page.locator('[data-connection-id]').count() == 8
            assert page.locator('[data-connection-id].selected').get_attribute('data-connection-id') == client_ids[0]
            assert page.get_by_label('Роль выбранного подключения', exact=True).input_value() == 'client'
            page.locator('[data-connection-id].selected summary').click()
            assert page.locator('[data-connection-id].selected .credential-history-row').count() == 11
            screenshot_widths = []
            for width in (1440, 1280, 1024, 390):
                page.set_viewport_size({'width': width, 'height': 1000})
                dimensions = page.evaluate('({viewport:innerWidth,content:document.documentElement.scrollWidth})')
                overlaps = page.locator('.client-row').evaluate_all('rows=>rows.filter(row=>{let a=row.querySelector("strong").getBoundingClientRect(),b=row.querySelector("button").getBoundingClientRect();return Math.min(a.right,b.right)>Math.max(a.left,b.left)+1 && Math.min(a.bottom,b.bottom)>Math.max(a.top,b.top)+1}).length')
                assert dimensions['content'] <= width + 2, dimensions
                assert overlaps == 0, {'width': width, 'overlaps': overlaps}
                screenshot_widths.append(width)
                page.screenshot(path=str(artifacts / f'connections-107-records-{width}.png'), full_page=True)
            page.set_viewport_size({'width': 1440, 'height': 1000})
            routes = [
                ('Серверы', 'agents'), ('Задачи', 'jobs'), ('MCP-менеджер', 'mcpmanage'),
                ('Вызов инструментов', 'tools'), ('Ресурсы', 'resources'),
                ('Резервирование', 'failover'), ('Аудит', 'audit'), ('Операции доступа', 'operations'),
                ('Инструкции', 'instructions'), ('Профили', 'profiles'),
                ('Вебхуки и агенты', 'webhooks'), ('Виртуальные MCP', 'capabilities'),
                ('Безопасность · opt-in', 'security'), ('Клиенты', 'clients'),
            ]
            for label, route in routes:
                page.get_by_role('link', name=label, exact=True).click()
                page.wait_for_url('**/#' + route)
                page.wait_for_timeout(100)
            page.get_by_role('link', name='Профили', exact=True).click()
            page.get_by_role('button', name='Новый профиль', exact=True).click()
            page.get_by_label('Идентификатор профиля', exact=True).fill('browser-ops')
            page.get_by_label('Название профиля', exact=True).fill('Browser operations')
            page.get_by_label('Режим доступа', exact=True).select_option('full')
            page.get_by_label('Режим выполнения', exact=True).select_option('unrestricted')
            page.get_by_label('Разрешённые цели', exact=True).fill('*')
            page.get_by_label('Разрешённые инструменты', exact=True).fill('*')
            with page.expect_response(lambda response: response.url.endswith('/admin/api/access-profiles/browser-ops') and response.request.method == 'PUT') as created:
                page.get_by_role('button', name='Создать профиль', exact=True).click()
            assert created.value.status == 200, created.value.text()[:1000]
            page.get_by_label('Название профиля', exact=True).wait_for()
            profile = page.request.get(url + '/admin/api/access-profiles/browser-ops').json()
            assert profile['approval_mode'] == 'unrestricted'
            assert not profile['workspace_refs']
            page.get_by_role('button', name='Добавить рабочее пространство', exact=True).click()
            page.get_by_label('Рабочее пространство 1: machine id', exact=True).fill('fixture')
            page.get_by_label('Рабочее пространство 1: путь', exact=True).fill('/work')
            page.get_by_label('Рабочее пространство 1: startup document', exact=True).fill('AGENTS.md')
            page.get_by_label('Рабочее пространство 1: shell target', exact=True).fill('shell:fixture')
            page.get_by_role('button', name='Сохранить профиль', exact=True).click()
            page.get_by_text('Профиль сохранён', exact=True).wait_for()
            page.reload()
            page.get_by_label('Рабочее пространство 1: путь', exact=True).wait_for()
            assert page.get_by_label('Рабочее пространство 1: путь', exact=True).input_value() == '/work'
            assert page.get_by_label('Режим выполнения', exact=True).input_value() == 'unrestricted'
            page.get_by_role('link', name='Клиенты', exact=True).click()
            page.get_by_label('Профиль нового подключения', exact=True).select_option('browser-ops')
            page.get_by_label('Роль нового подключения', exact=True).select_option('admin')
            page.get_by_role('button', name='Выдать managed token', exact=True).click()
            page.locator('.token-callout code').wait_for()
            issued = page.locator('.token-callout code').inner_text()
            assert issued.startswith('gptk_')
            page.get_by_role('link', name='Токены и подключение', exact=True).click()
            assert page.get_by_label('Выданный токен подключения').input_value() == issued
            page.get_by_role('link', name='Клиенты', exact=True).click()
            assert page.locator('.token-callout code').inner_text() == issued
            server.terminate()
            server.wait(timeout=10)
            server = subprocess.Popen([str(artifacts / 'ui-canary'), str(fixture), address], stdout=log, stderr=log)
            for _ in range(100):
                try:
                    urllib.request.urlopen(url + '/healthz', timeout=1).close()
                    break
                except Exception:
                    time.sleep(0.1)
            page.goto(url + '/admin/#clients')
            page.get_by_role('button', name='Выбрать managed-client', exact=True).click()
            page.get_by_role('button', name='Показать сохранённый токен', exact=True).click()
            page.locator('.token-callout code').wait_for()
            assert page.locator('.token-callout code').inner_text() == issued
            assert page.get_by_label('Роль выбранного подключения', exact=True).input_value() == 'admin'
            profile = page.request.get(url + '/admin/api/access-profiles/browser-ops').json()
            assert profile['workspace_refs'][0]['workspace_path'] == '/work'
            operations = page.request.get(url + '/admin/api/operations').json()
            assert operations['total'] >= 3
            assert all(item['status'] == 'completed' for item in operations['operations'])
            page.goto(url + '/admin/#operations')
            page.get_by_role('heading', name='Операции доступа', exact=True).wait_for()
            page.get_by_text('Создание подключения', exact=True).wait_for()
            page.goto(url + '/admin/legacy/')
            page.wait_for_url('**/admin/#overview')
            page.locator('#status').filter(has_text='online').wait_for()
            page.set_viewport_size({'width': 390, 'height': 844})
            page.screenshot(path=str(artifacts / 'unified-mobile.png'), full_page=True)
            dimensions = page.evaluate('({width:innerWidth, content:document.documentElement.scrollWidth})')
            assert dimensions['content'] <= dimensions['width'] + 2, dimensions
            assert not errors, errors
            browser.close()
        print(json.dumps({'result': 'PASS', 'inventory_records': 107, 'logical_oauth_connections': 8, 'no_overlap_widths': screenshot_widths, 'task_result_and_error_visible': True, 'routes': len(routes), 'real_go_hub': True, 'profile_edit_and_restart': True, 'operations': operations['total'], 'token_retained_on_navigation': True, 'saved_token_after_restart': True, 'admin_role_visible': True, 'legacy_redirect': True, 'mobile': dimensions, 'page_errors': errors}, ensure_ascii=False))
    finally:
        server.terminate()
        try:
            server.wait(timeout=10)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()
        log.close()


if __name__ == "__main__":
    main()
