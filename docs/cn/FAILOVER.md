# 故障转移和降级恢复

GPTAdmin 的设计使得一台死机并不一定意味着控制平面死机。回退节点可以通过相同的公共隧道路径保持备用集线器可访问，因此服务可以在降级模式下保持足够长的时间来检查日志、到达幸存的服务器并修复主服务器。

这不是神奇的多主复制。当主数据库关闭时，一些新的内存状态可能会过时或暂时不可用。重要的部分是 GPTAdmin 将操作跟踪存储在磁盘上：排队/后台作业、大型 stdout/stderr 溢出文件、shellmcp 假脱机文件、发件箱响应、注册表快照、故障转移运行时状态和服务日志。这意味着系统可能会失去一点即时性，但恢复工作并不是盲目的，大多数工作证据也不会永远消失。

## 角色

|角色 |它有什么作用 |
|------|--------------|
| **主要枢纽** |正常活动的 GPTAdmin 中心、管理 UI、MCP 中继、Action/OpenAPI 端点、队列和路由。 |
| **后备节点** |运行备用集线器或本地代理，监视公共卫生 URL，并可以在主集线器停止应答时提升自身。 |
| **隧道** |公众进入。在发生事件时，它会重新指向回退，因此客户端仍然具有 HTTPS 入口点。 |
| **回收路径** |当主服务器恢复时，它会向活动后备服务器发送签名的回收/降级请求，以便后备服务器再次成为客户端/备用服务器。 |

## 当主进程死亡时会发生什么

1. 后备看门狗按一定时间间隔检查主要公共卫生端点。
2. 在配置的故障阈值之后，回退确认公共 URL 仍然不健康。
3. 后备保持其本地集线器和回收感知代理处于活动状态，然后为每个配置的端点启动一个 FRP 客户端。这是必需的，因为现代 `frpc` 接受每个进程/配置一个客户端服务器块。
4. 现有的 AI 客户端和管理员继续通过公共 URL 访问 GPTAdmin，但系统处于降级模式。
5. 幸存的 shell/MCP 服务器重新连接或继续轮询。失效的服务器显示为离线/过时，可以从后备控制平面进行修复。
6. 当主节点再次恢复正常时，主节点会发送签名的回收消息。活动回退降级并返回到待机/客户端模式。

## 在降级模式下什么仍然有效

- 后备的管理 UI 和运行状况端点。
- 针对仍可从回退中访问或已通过长轮询连接的服务器的 Shell/MCP 操作。
- 读取持久保存到处理这些作业的节点上的磁盘上的排队/后台作业状态。
- 通过假脱机文件输出大型命令，而不是在聊天上下文中丢失它。
- 当传输重新连接时，基于发件箱的排队响应传送。
- 事件分类：日志、服务状态、隧道状态和恢复命令。

## 哪些内容可能会暂时失效或丢失

- 最近的内存计数器、活动请求列表或尚未持久或同步的上次看到的数据。
- 在发生故障的那一刻停止的机器上运行作业。它们最后保留的假脱机/日志状态仍然有用，但进程本身可能需要重新运行。
- 仅存在于失效服务器上的本地资源。
- MCP 工具托管在失效的服务器上，直到该服务器恢复或另一个节点托管相同的工具。

GPTAdmin 将此视为优雅降级：首先保持较小的控制平面处于活动状态，然后用它来恢复较大的控制平面。

## 操作员清单

在主节点上：

```bash
gptadmin urls
systemctl status gptadmin-hub gptadmin-tunnel-frpc --no-pager
journalctl -u gptadmin-hub -n 120 --no-pager
```

在后备节点上，检查看门狗/代理运行时：

```bash
ps aux | grep -E 'gptadmin|failover|frpc' | grep -v grep
curl -fsS http://127.0.0.1:9001/healthz
curl -fsS http://127.0.0.1:9101/healthz
```

然后在积极删除或重新启动之前检查持久状态：

```bash
find /var/lib/gptadmin -maxdepth 4 -type f | sort | tail -100
find /data/gptadmin-failover -maxdepth 4 -type f | sort | tail -100
```

查找假脱机、发件箱、运行时和日志文件。它们是恢复路径。

## 当前 HAOS 风格的布局

支持的 HAOS 附加组件拥有完整的后备运行时：

```text
/opt/gptadmin/failover/failover_config.json
/opt/gptadmin/failover/failover_state.json
/usr/local/bin/gptadmin_failover_runtime.py
/usr/local/bin/gptadmin_failover_proxy.py  # 0.0.0.0:9101 -> 127.0.0.1:9001
/usr/local/bin/gptadmin_failover_watchdog.py
/usr/local/bin/frpc                     # linux/arm64
/data/config/frpc-failover-1.toml       # one file/process per FRP endpoint
/data/config/failover_frpc.pid
```即使在升级之前，该附加组件也可以使备用集线器和代理保持可用。
它的无 systemd 运行时运行看门狗循环，仅在之后升级
阈值/确认，并在主返回时接受签名回收。
部署源必须提供真正的Linux/ARM64 `frpc`；复制
主 x86-64 二进制文件会默默地生成一个不工作的备用文件。

## 黑盒回归覆盖率

Docker套件在CI中运行，无需部署FRP即可在本地运行
服务器：

```bash
docker compose -f tests/e2e/failover/docker-compose.yml up --build --abort-on-container-exit --exit-code-from failover-e2e
```

它在测试组合中断之前分别验证这些边界：

- 仅隧道损失并不能促进健康的初级；
- 主集线器丢失会促进通过实时隧道的回退；
- 隧道恢复后集线器和隧道同时丢失恢复；
- 签署的主要回收删除了恢复后的后备路由。
- 通过健康的公共端点，排名 1 晋升围栏排名 2；
- 当等级 1 不可用时，等级 2 仅在其较长阈值后晋升。

该套件使用真正的 Go 集线器、看门狗和代理进程。它的本地入口
并且 FRP 客户端将存储库拥有的故障转移合约与
外部 FRP 服务。它不会取代物理 HAOS 演练：即演练
必须验证 `:9001`、`:9101`、每个配置的端点一个实时 `frpc` 进程，
停止`server-100`后的公共卫生路线，并在之后签署回收
初级回报。

## 设计规则

比起“完美而堕落”，更喜欢“活着但堕落”。在中断期间，GPTAdmin 应该保持足够的自身可访问性，以回答：什么是活动的、什么是死亡的、哪些作业正在运行、日志在哪里以及如何恢复主数据库。
