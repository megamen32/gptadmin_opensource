from cli import frpc_wrapper_script


def test_frpc_wrapper_supervises_each_edge_independently() -> None:
    script = frpc_wrapper_script("/opt/gptadmin/bin/frpc", {"FRP_SERVER_ENDPOINTS": "primary=a:7000,vpn2=b:7001,vusa=c:7002"})

    assert "restart" in script.lower()
    assert "next_retry" in script
    assert "must not stop healthy" in script.lower()
    assert "exit $?" not in script
