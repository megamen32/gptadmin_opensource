import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PLUGIN = ROOT / "plugins" / "gptadmin"


def test_gptadmin_plugin_manifest_declares_oauth_mcp_and_workflow_skills():
    manifest = json.loads((PLUGIN / ".codex-plugin" / "plugin.json").read_text())
    assert manifest["name"] == "gptadmin"
    assert manifest["version"] == "1.4.0"
    assert manifest["mcpServers"] == "./.mcp.json"
    assert manifest["skills"] == "./skills/"
    assert "OAuth" in manifest["interface"]["longDescription"]
    assert "password" in manifest["interface"]["longDescription"]
    assert {"gptadmin-connect", "gptadmin-mcp-install", "gptadmin-workflow"} == {
        path.name for path in (PLUGIN / "skills").iterdir() if path.is_dir()
    }


def test_gptadmin_mcp_config_is_remote_https_without_credentials():
    config = json.loads((PLUGIN / ".mcp.json").read_text())
    server = config["mcpServers"]["gptadmin"]
    assert server["type"] == "http"
    assert server["url"].startswith("https://")
    assert server["url"].endswith("/mcp")
    assert not any("token" in key.lower() or "password" in key.lower() for key in server)
    assert server["url"] == "https://mcp.bezrabotnyi.com/mcp"


def test_gptadmin_marketplace_entry_is_installable_and_points_at_plugin():
    marketplace = json.loads(
        (ROOT / ".agents" / "plugins" / "marketplace.json").read_text()
    )
    assert marketplace["name"] == "gptadmin-local"
    entry = next(item for item in marketplace["plugins"] if item["name"] == "gptadmin")
    assert entry["source"] == {"source": "local", "path": "./plugins/gptadmin"}
    assert entry["policy"] == {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL",
    }
    assert entry["category"] == "Developer Tools"


def test_portable_plugin_matches_native_version_and_fixed_layout():
    portable = json.loads((PLUGIN / 'plugin.json').read_text())
    native = json.loads((PLUGIN / '.codex-plugin' / 'plugin.json').read_text())
    assert portable['$schema'] == 'https://agent-plugins.org/schemas/1.0.0/plugin.schema.json'
    assert portable['version'] == native['version'] == '1.4.0'
    assert portable['name'] == 'gptadmin'
    assert set(portable) <= {'$schema', 'name', 'version', 'description', 'author', 'homepage', 'repository', 'license', 'keywords', 'extensions'}
    config = json.loads((PLUGIN / 'mcp.json').read_text())
    assert config['$schema'] == 'https://agent-plugins.org/schemas/1.0.0/mcp.schema.json'
    server = config['mcpServers']['gptadmin']
    assert server['type'] == 'streamable-http'
    assert server['url'] == 'https://mcp.bezrabotnyi.com/mcp'
    assert server['headers'] == {'X-GPTAdmin-Result-Mode': 'text'}
    assert not (PLUGIN / 'AGENTS.md').exists()


def test_profile_workflow_retains_text_with_context_and_does_not_autowrite():
    skill = (PLUGIN / 'skills' / 'gptadmin-workflow' / 'SKILL.md').read_text()
    for term in ('search', 'instructions_required', 'executed:false', 'compaction', 'AGENTS.md'):
        assert term in skill
    assert 'Keep the instruction text' in skill
    assert 'overwrite' in skill
