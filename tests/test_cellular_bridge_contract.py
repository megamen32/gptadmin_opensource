from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
APP = ROOT / "android-cellular-proxy" / "app"


def test_bridge_declares_boot_persistence_and_cellular_only_transport():
    manifest = (APP / "src/main/AndroidManifest.xml").read_text()
    service = (APP / "src/main/java/com/gptadmin/cellularproxy/CellularProxyService.java").read_text()

    assert "android.permission.RECEIVE_BOOT_COMPLETED" in manifest
    assert "BootReceiver" in manifest
    assert "CellularProxyService" in manifest
    assert "MainActivity" in manifest
    assert "specialUse" in manifest
    assert "FOREGROUND_SERVICE_SPECIAL_USE" in manifest
    assert "dataSync" not in manifest
    assert "TRANSPORT_CELLULAR" in service
    assert "requestNetwork" in service
    assert "getSocketFactory" in service
    assert "FOREGROUND_SERVICE_TYPE_SPECIAL_USE" in service


def test_launcher_enables_boot_receiver_without_adb_service_access():
    launcher = (APP / "src/main/java/com/gptadmin/cellularproxy/MainActivity.java").read_text()

    assert "startForegroundService" in launcher
    assert "finish()" in launcher


def test_bridge_exposes_only_connect_and_socks_on_the_lan_listener():
    service = (APP / "src/main/java/com/gptadmin/cellularproxy/CellularProxyService.java").read_text()

    assert '"0.0.0.0"' in service
    assert "ALLOWED_CLIENT" not in service
    assert "handleSocks5" in service
    assert "handleConnect" in service
    assert "HTTP/1.1 200 Connection Established" in service
    assert '"HTTP/1.1 200 Connection Established\\\\r' not in service


def test_bridge_exposes_source_restricted_cellular_health_evidence():
    service = (APP / "src/main/java/com/gptadmin/cellularproxy/CellularProxyService.java").read_text()

    assert '"/health"' in service
    assert "getLinkProperties" in service
    assert "interface" in service


def test_bridge_bounds_clients_and_cleans_failed_cellular_candidates():
    service = (APP / "src/main/java/com/gptadmin/cellularproxy/CellularProxyService.java").read_text()

    assert "ThreadPoolExecutor" in service
    assert "ArrayBlockingQueue" in service
    assert "setSoTimeout" in service
    assert "closeQuietly(socket)" in service
