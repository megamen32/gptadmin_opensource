"""Local partition logic only; execution gates still run real test commands."""
from types import SimpleNamespace
import pytest
from tools.ci_test_shards import PytestShard, selected_names, shard_for


@pytest.mark.parametrize("count", [1, 2, 3, 5])
def test_every_discovered_name_has_exactly_one_owner(count):
    names = [f"TestDiscovered{i}" for i in range(100)] + ["Example_value", "FuzzParser"]
    groups = [set(selected_names(names, index, count)) for index in range(count)]
    assert set.union(*groups) == set(names)
    assert sum(map(len, groups)) == len(names)
    assert [selected_names(reversed(names), index, count) for index in range(count)] == [sorted(group) for group in groups]


@pytest.mark.parametrize("index,count", [(-1, 2), (2, 2), (0, 0)])
def test_invalid_shard_is_rejected(index, count):
    with pytest.raises(ValueError):
        selected_names(["TestOne"], index, count)


def test_pytest_keeps_file_fixtures_together_and_reports_omitted_items():
    original = [SimpleNamespace(nodeid=f"tests/test_file{i}.py::test_case{j}")
                for i in range(10) for j in range(3)]
    groups = []
    for index in range(2):
        deselected = []
        hook = SimpleNamespace(pytest_deselected=lambda items: deselected.extend(items))
        items = list(original)
        PytestShard(index, 2).pytest_collection_modifyitems(SimpleNamespace(hook=hook), items)
        assert {id(item) for item in items + deselected} == {id(item) for item in original}
        groups.append(items)
    assert len(groups[0]) + len(groups[1]) == len(original)
    for i in range(10):
        expected = [item for item in original if item.nodeid.startswith(f"tests/test_file{i}.py::")]
        assert all(item in groups[shard_for(f"tests/test_file{i}.py", 2)] for item in expected)
