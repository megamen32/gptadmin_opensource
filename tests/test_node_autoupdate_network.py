"""A truncated release index must not abort a safe retry or use partial JSON."""
import http.client
import time
from unittest.mock import patch
import pytest
from scripts import gptadmin_node_autoupdate as updater


def test_truncated_release_index_is_retried_in_full():
    deadline = time.monotonic() + 10
    with patch.object(updater, "get", side_effect=[http.client.IncompleteRead(b"partial", 1), b"[]"]) as get:
        assert updater.get_release_index(deadline) == b"[]"
    assert get.call_count == 2
    assert all(call.args == (updater.API, 2 * 1024**2, deadline) for call in get.call_args_list)


def test_second_truncated_index_fails_closed():
    with patch.object(updater, "get", side_effect=http.client.IncompleteRead(b"partial", 1)) as get:
        with pytest.raises(http.client.IncompleteRead):
            updater.get_release_index(time.monotonic() + 10)
    assert get.call_count == 2
