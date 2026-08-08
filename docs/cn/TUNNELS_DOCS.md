# 隧道

该集线器需要可通过互联网访问（以便您的 AI/MCP 客户端可以
连接）。您不需要自己的域 - GPT‑Админ 支持两个自动隧道。

## 玻璃钢（默认）

[FRP](https://github.com/fatedier/frp) 是一个快速反向代理。 GPT‑Админ 运行
公共 FRP 服务器；安装程序可以自动注册。

### 设置

在 `gptadmin setup` 期间，选择选项 **1**（通过 FRP 自动隧道）。安装程序：
1.下载FRP客户端
2. 在公共FRP服务器上注册一个随机子域
3. 将 FRP 客户端作为服务与集线器一起启动
4. 打印您的公共 URL：`https://random-sub.frp.bezrabotnyi.com`

### 优点/缺点

- ✅ 无需域名，无需设置 DNS
- ✅ 快速（直接 TCP 隧道）
- ⚠️ URL 位于 `frp.bezrabotnyi.com`（共享域）
- ⚠️ 免费的 FRP 服务器有速率限制

## Cloudflare 隧道

[Cloudflare 隧道](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
创建通往 Cloudflare 边缘的安全出站隧道。您需要一个 Cloudflare
Cloudflare 上的帐户和域。

### 设置

在 `gptadmin setup` 期间，选择 Cloudflare 选项。或者稍后配置：

```bash
gptadmin tunnel cloudflare
```

你需要：
- `CLOUDFLARE_TOKEN` — 具有隧道权限的 Cloudflare API 令牌
- 由 Cloudflare 管理的域

命令行界面：
1.安装`cloudflared`
2. 创建隧道
3. 将其绑定到您的 Cloudflare 域上的子域
4. 将 `cloudflared` 作为服务启动
5. 打印您的公共 URL：`https://hub.yourdomain.com`

### 优点/缺点

- ✅ 您自己的域名
- ✅ Cloudflare 的 DDoS 防护 + 边缘缓存
- ✅ 您的服务器上不需要入站端口
- ⚠️ 需要 Cloudflare 帐户 + 域名

## 你自己的域（nginx + Certbot）

如果您已经拥有具有公共 IP 和域的服务器：

1. 将 DNS A 记录指向您的服务器
2.使用提供的nginx配置模板：`deploy/nginx/`（复制并编辑）
3. 获取证书：`certbot --nginx -d hub.yourdomain.com`
4. 在本地主机上运行集线器，nginx 代理它

```bash
# Example nginx location block
location / {
    proxy_pass http://127.0.0.1:25900;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";  # for MCP SSE
}
```

## 选择哪个？

|使用案例 |推荐|
|----------|-------------|
|快速启动，无域名| FRP（自动隧道）|
|拥有自己的域名，想要 DDoS 防护 | Cloudflare 隧道 |
|已经拥有服务器+域名 | nginx + 证书机器人 |
|仅限本地开发 |无（使用 `localhost:25900`）|

## 另请参阅

- [入门](./GETTING_STARTED.md)
- [配置](./CONFIGURATION.md) — `PUBLIC_ORIGIN` 等。
- [集线器](./HUB.md)
