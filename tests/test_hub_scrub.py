import pytest
from hub_proxy import _mask, scrub_headers, scrub_query, scrub_payload

def test_mask_none():
    assert _mask(None) is None

def test_mask_empty():
    assert _mask('') == ''

def test_mask_short_string():
    assert _mask('abc') == '***'
    assert _mask('12345678') == '***'

def test_mask_long_string():
    result = _mask('secret_token_12345')
    # Should keep first 2 and last 2 chars, with ellipsis in middle
    assert result.startswith('se')
    assert result.endswith('45')
    assert '…' in result
    assert len(result) < len('secret_token_12345')

def test_scrub_headers_removes_sensitive():
    # Only test keys that are actually in SENSITIVE_KEYS
    headers = {
        'Authorization': 'Bearer secret123',
        'shellmcp_token': 'token456',
        'token': 'tok789',
        'ctl_token': 'ctl123',
        'Content-Type': 'application/json'
    }
    result = scrub_headers(headers)
    assert result['Content-Type'] == 'application/json'
    # Sensitive headers should be masked
    assert result['Authorization'] != 'Bearer secret123'
    assert result['shellmcp_token'] != 'token456'
    assert result['token'] != 'tok789'
    assert result['ctl_token'] != 'ctl123'

def test_scrub_headers_preserves_safe():
    headers = {'Content-Type': 'text/html', 'Accept': '*/*', 'X-API-Key': 'key123'}
    result = scrub_headers(headers)
    # X-API-Key is not in SENSITIVE_KEYS, so it should be preserved
    assert result == headers

def test_scrub_query_removes_sensitive():
    query = [('token', 'secret'), ('page', '1'), ('authorization', 'Bearer abc')]
    result = scrub_query(query)
    # Should preserve page
    assert ('page', '1') in result
    # Should mask token and authorization
    token_pair = next((k, v) for k, v in result if k == 'token')
    assert token_pair[1] != 'secret'
    auth_pair = next((k, v) for k, v in result if k == 'authorization')
    assert auth_pair[1] != 'Bearer abc'

def test_scrub_payload_dict():
    # Only test keys that are actually in SENSITIVE_KEYS
    payload = {
        'username': 'admin',
        'token': 'secret123',
        'data': {'authorization': 'Bearer key456', 'value': 'safe'}
    }
    result = scrub_payload(payload)
    assert result['username'] == 'admin'
    assert result['token'] != 'secret123'
    assert result['data']['authorization'] != 'Bearer key456'
    assert result['data']['value'] == 'safe'

def test_scrub_payload_list():
    payload = [{'token': 'abc'}, {'safe': 'value'}]
    result = scrub_payload(payload)
    assert result[0]['token'] != 'abc'
    assert result[1]['safe'] == 'value'

def test_scrub_payload_string():
    assert scrub_payload('just a string') == 'just a string'

def test_scrub_payload_none():
    assert scrub_payload(None) is None
