# 故障处置文档：登录爆破绕过封禁与会话绑定异常（2026-07-17）

> 服务器：VoyraCloud VPS（Ubuntu 24.04）
> 部署路径：`~/docker/sub2api`
> 当前状态：源站入口隔离和 Full (strict) 回源已完成；真实客户端 IP、自动封禁和会话绑定仍待生产验证。
> 文档职责：保留本次事件的事实、根因、处置步骤、验证证据和回滚决策；长期基线见 [服务器安全配置指南](../../docs/server-security-guide.md)。

## 1. 当前进展

### 1.1 已完成

- 已将数据库设置 `session_binding_enabled` 临时改为 `false`，用户恢复正常登录。
- 已确认公网攻击入口：sub2api 通过 Docker 映射 `0.0.0.0:80->8080/tcp` 直接暴露。
- 已验证纯 UFW INPUT 规则无法拦截该 Docker DNAT/FORWARD 流量。
- 已安装并启动宿主机 Caddy `v2.11.4`，接管 80/443 并使用 Cloudflare Origin Certificate。
- 本地源码已同步上游 `v0.1.159`，包含提交 `7c48f9a85` 的安全客户端 IP统一修复。
- 自动封禁功能已适配上游的 `middleware.SecurityClientIP()`，相关测试通过并已提交；私有镜像 `linshisancc/sub2api:v1.0.23` 已推送，尚未部署生产。
- 已确认 `proxy.linshisan.cc` 必须保持灰云，因为 VPN 客户端使用该域名的 `8443` 端口。
- sub2api 已改为仅映射 `127.0.0.1:8080->8080/tcp`，不再公开 Docker HTTP 端口。
- UFW 的 80/443 已仅允许 Cloudflare 官方 IPv4/IPv6 网段，Cloudflare 已切换 Full (strict)。
- 已从服务器外部确认裸 IP 和灰云域名的 80/443 均超时，无法绕过 Cloudflare 访问 sub2api。
- 已确认 VPN 和 sub2api 是两个独立入口：VPN 继续监听 `0.0.0.0:8443`，客户端连接正常。

### 1.2 尚未完成

- `api_key_acl_trust_forwarded_ip` 尚未完成生产验证。
- 会话绑定和自动封禁尚未在生产重新启用。

## 2. 已确认的生产拓扑

当前 `docker compose ps`：

```text
sub2api   linshisancc/sub2api:latest   127.0.0.1:8080->8080/tcp
postgres                                 5432/tcp
redis                                    6379/tcp
```

服务器实际 Compose 配置：

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

VPN 独立监听：

```text
0.0.0.0:8443   docker-proxy
```

宿主机 Caddy 监听 80/443 并反向代理到 `127.0.0.1:8080`。容器内仍监听 8080，公网无法直接访问该 Docker 映射。

## 3. 事件与根因

### 3.1 登录爆破绕过封禁

攻击 IP `111.229.153.119` 在已配置 UFW deny 和 Cloudflare WAF 后仍产生请求记录。已从外部验证：

```bash
curl http://62.132.18.242/health
```

直接返回 `200 OK`。

实际攻击路径高度符合：

```text
攻击者
  -> 62.132.18.242:80（或 proxy.linshisan.cc:80）
  -> Docker DNAT/FORWARD
  -> sub2api:8080
  -> POST /api/v1/auth/login
```

Docker 发布端口使用 iptables DNAT/FORWARD 链，流量不经过 UFW 主要保护的 INPUT 链，因此：

- Cloudflare WAF 对裸 IP或灰云直连流量不可见。
- 事发时 UFW 的 80 端口 Cloudflare-only 规则无法拦截 Docker 发布端口。
- 单独删除灰云 DNS 也不能解决问题，因为攻击者已经知道公网 IP。

### 3.2 全体用户会话立即过期

提交 `0ddd58aaf` 新增会话 IP/UA 绑定并默认开启。登录时和后续请求使用的客户端 IP不稳定，导致 JWT 中的绑定哈希与请求侧重新计算的哈希不一致，返回：

```text
HTTP 401 SESSION_BINDING_MISMATCH
```

上游 `v0.1.159` 的提交 `7c48f9a85` 已将以下安全场景统一为同一客户端 IP来源：

- 会话绑定签发与校验
- 操作审计日志
- API Key IP ACL
- 本地自动封禁功能（合并后适配）

统一入口是 `middleware.SecurityClientIP()`，其行为受系统设置 `api_key_acl_trust_forwarded_ip` 控制：

- `false`（默认）：使用 Gin `trusted_proxies` 可信代理链。
- `true`：按 `CF-Connecting-IP`、`X-Real-IP`、`X-Forwarded-For` 的顺序信任转发头。

此修复解决了不同安全模块读取不同 IP的问题，但不会自动建立可信网络边界。在源站仍能直连时，不得直接开启“信任转发 IP”，否则攻击者可以伪造请求头。

## 4. 已落地架构

```text
普通用户
  -> www.linshisan.cc（Cloudflare 橙云）
  -> 宿主机 Caddy :80/:443
  -> 127.0.0.1:8080
  -> sub2api 容器 :8080

VPN 客户端
  -> proxy.linshisan.cc（DNS-only 灰云）
  -> 宿主机 :8443
  -> VPN 容器
```

关键边界：

- `proxy.linshisan.cc` 灰云记录继续保留，VPN 8443 不做改动。
- sub2api 不再占用公网 80，也不暴露任何 Docker 公网端口。
- Caddy只为 `www.linshisan.cc` 提供 sub2api，不为 `proxy.linshisan.cc` 提供网站。
- UFW 的 80/443 只允许 Cloudflare 官方网段。
- 8443 继续公网开放，由 VPN 协议自身认证保护。
- 灰云会暴露源站 IP，但源站 IP的 80/443 已被防火墙保护，不再形成 sub2api 绕过入口。

## 5. 正式处置步骤

### 步骤 0（已完成）：关闭会话绑定

```bash
docker compose -f ~/docker/sub2api/docker-compose.yml exec postgres \
  psql -U sub2api -d sub2api \
  -c "UPDATE settings SET value='false' WHERE key='session_binding_enabled';"
```

确认设置：

```bash
docker compose -f ~/docker/sub2api/docker-compose.yml exec postgres \
  psql -U sub2api -d sub2api \
  -c "SELECT key, value FROM settings WHERE key IN ('session_binding_enabled', 'api_key_acl_trust_forwarded_ip');"
```

### 步骤 1（已完成）：准备证书和 Caddyfile

在 Cloudflare 为 `www.linshisan.cc` 生成 Origin Certificate，证书和私钥保存到：

```text
/etc/caddy/certs/www.linshisan.cc.pem
/etc/caddy/certs/www.linshisan.cc.key
```

私钥权限限制为 Caddy 服务用户可读。迁移期间 Cloudflare 仍处于 Flexible，因此 Caddyfile 同时提供明文 80 回源和 TLS 443 回源，并显式关闭自动 HTTP 到 HTTPS 重定向，避免形成 Flexible 重定向循环。

建议配置结构：

```caddyfile
{
	auto_https disable_redirects
}

(sub2api_backend) {
	reverse_proxy 127.0.0.1:8080 {
		header_up X-Real-IP {http.request.header.CF-Connecting-IP}
		header_up X-Forwarded-For {http.request.header.CF-Connecting-IP}
		header_up X-Forwarded-Proto {http.request.header.X-Forwarded-Proto}
		header_up CF-Connecting-IP {http.request.header.CF-Connecting-IP}
	}
}

http://www.linshisan.cc {
	import sub2api_backend
}

https://www.linshisan.cc {
	tls /etc/caddy/certs/www.linshisan.cc.pem /etc/caddy/certs/www.linshisan.cc.key
	import sub2api_backend
}
```

先验证配置，不启动：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
```

### 步骤 2（已完成）：补充 443 的 Cloudflare-only 防火墙规则

切换前 80 已配置 Cloudflare IPv4/IPv6 网段，443 需要补充相同来源规则。执行前先确认默认入站策略为 deny：

```bash
sudo ufw status verbose
```

添加 443 IPv4：

```bash
for ip in 173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13 131.0.72.0/22; do
  sudo ufw allow from "$ip" to any port 443 proto tcp
done
```

添加 443 IPv6：

```bash
for ip in 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32 2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32; do
  sudo ufw allow from "$ip" to any port 443 proto tcp
done
```

不要删除 8443 的公网放行规则；它属于 VPN。

### 步骤 3（已完成）：切换 sub2api 到回环地址并启动 Caddy

先备份：

```bash
cd ~/docker/sub2api
cp docker-compose.yml docker-compose.yml.before-caddy
sudo cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-sub2api
```

把 sub2api 的端口映射改为：

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

端口映射变化必须重建容器，单纯 `restart` 不生效：

```bash
docker compose up -d --no-deps --force-recreate sub2api
curl -fsS http://127.0.0.1:8080/health
sudo systemctl enable --now caddy
sudo systemctl status caddy --no-pager
```

确认监听关系：

```bash
sudo ss -tlnp | grep -E ':(80|443|8080|8443)\b'
```

预期：

```text
80/443       caddy
127.0.0.1:8080 docker-proxy
0.0.0.0:8443 docker-proxy（VPN，保持不变）
```

### 步骤 4（已完成）：验证 Caddy和 Cloudflare，切换 Full (strict)

当时不能从 Flexible 直接切换：源站尚无 443 监听，直接切换后 Cloudflare 会改用 HTTPS 回源并失败，网站通常会出现 `525 SSL handshake failed`、`526 Invalid SSL certificate` 或 `521 Web server is down`。

必须先保持 Flexible，完成步骤 1 至步骤 3，让 Caddy 同时监听 80 和 443。此时 Cloudflare 仍通过 80 回源，现有网站可以继续工作；Caddy 的 HTTP 站点块暂时不能强制跳转 HTTPS。

先验证 Flexible 链路仍正常：

```bash
curl -fsSI https://www.linshisan.cc
```

再在源站本机绕过 Cloudflare，单独验证 Caddy 的 HTTPS 虚拟主机和证书：

```bash
curl -kfsS --resolve www.linshisan.cc:443:127.0.0.1 \
  https://www.linshisan.cc/health

openssl x509 -in /etc/caddy/certs/www.linshisan.cc.pem \
  -noout -subject -issuer -dates -ext subjectAltName
openssl x509 -in /etc/caddy/certs/www.linshisan.cc.pem \
  -noout -checkhost www.linshisan.cc

sudo journalctl -u caddy -n 100 --no-pager
```

只有以下切换闸门全部通过，才将 Cloudflare SSL/TLS 加密模式改为 **Full (strict)**，不要选择仅校验证书较弱的普通 Full：

1. `www.linshisan.cc` 在 Flexible 下仍可正常登录和调用 API。
2. Caddy 同时监听 80/443，sub2api 只监听 `127.0.0.1:8080`。
3. 本机 `--resolve` 的 HTTPS 健康检查成功。
4. Origin Certificate 未过期，SAN 包含 `www.linshisan.cc`，私钥可被 Caddy 读取。
5. UFW 443 已放行最新 Cloudflare IPv4/IPv6 官方网段。
6. 同一个 Cloudflare Zone 下的其他橙云记录也已具备可用的源站 HTTPS；Cloudflare 的 Zone 级模式切换可能同时影响它们。灰云 `proxy.linshisan.cc` 不受该模式影响。

切换后验证：

```bash
curl -fsSI https://www.linshisan.cc
curl -fsS https://www.linshisan.cc/health
```

同时观察 Caddy 日志和 Cloudflare 返回码。如果出现 521/525/526，立即把 Cloudflare 模式恢复为 Flexible；因为 Caddy 的 80 站点仍保留，恢复后网站可以重新走 HTTP 回源。不要为排障重新公开 Docker 的 `0.0.0.0:80->8080` 映射。

同时从服务器外部验证：

```bash
curl -m 5 http://62.132.18.242/health
curl -k -m 5 https://62.132.18.242/health
curl -m 5 http://proxy.linshisan.cc/health
curl -k -m 5 https://proxy.linshisan.cc/health
```

以上 80/443 直连请求应超时或被拒绝。VPN 客户端连接 `proxy.linshisan.cc:8443` 应保持正常。

生产验证结果：四条外部直连测试均超时，`www.linshisan.cc` 经 Cloudflare 返回 HTTP 200，`/health` 返回 `{"status":"ok"}`，Caddy 日志无回源错误，VPN 客户端连接 8443 正常。

### 步骤 5：启用统一的真实客户端 IP来源

前提：

- sub2api 只绑定 `127.0.0.1:8080`。
- Caddy 80/443 只接受 Cloudflare 回源。
- Caddy 保留 Cloudflare 的 `CF-Connecting-IP`。

部署包含上游提交 `7c48f9a85` 的新版本后，优先在管理后台开启 `api_key_acl_trust_forwarded_ip`，确保应用内存配置同步更新。

如果必须直接操作数据库，使用 UPSERT 并重启 sub2api：

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "INSERT INTO settings (key, value, updated_at) VALUES ('api_key_acl_trust_forwarded_ip', 'true', NOW()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW();"
docker compose restart sub2api
```

重启后必须查询最终值，并确认网站恢复正常。

验证审计日志中的 `client_ip`：

- 应为真实用户公网 IP。
- 不应为 `127.0.0.1`。
- 不应为 Cloudflare 边缘网段。
- 伪造的客户端转发头不能从公网绕过，因为公网只能先经过 Cloudflare。

### 步骤 6：部署并启用自动封禁

自动封禁是应用层补充措施，不能替代步骤 1-5。由于代码在设置缺失时默认开启，首次部署新镜像前必须预置为关闭：

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "INSERT INTO settings (key, value, updated_at) VALUES ('feishu_login_bruteforce_autoban_enabled', 'false', NOW()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW();"
```

确认安全客户端 IP正确后再开启。默认策略为：5 分钟内同一 IP登录失败 10 次，临时封禁 60 分钟，并可发送飞书告警。详细说明见 [登录爆破自动封禁与飞书 Webhook 告警](LOGIN_BRUTEFORCE_AUTOBAN_AND_FEISHU_WEBHOOK.md)。

### 步骤 7：恢复会话绑定

只有在真实 IP、审计日志和自动封禁均验证正确后，才能重新开启：

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "UPDATE settings SET value='true' WHERE key='session_binding_enabled';"
```

开启后使用真实账号完成：登录、刷新页面、刷新 Token、等待数分钟后继续操作。确认不再出现 `SESSION_BINDING_MISMATCH`。

## 6. 验证清单

- [x] 临时关闭会话绑定，用户恢复登录
- [x] 确认 Docker 公网映射 `0.0.0.0:80->8080`
- [x] 确认 UFW-only 对 Docker 发布端口无效
- [x] 确认 VPN 独立使用灰云域名的 8443
- [x] Origin Certificate 已安装
- [x] Caddy 配置验证通过并接管 80/443
- [x] sub2api 仅监听 `127.0.0.1:8080`
- [x] 裸 IP和 `proxy.linshisan.cc` 的 80/443 无法访问 sub2api
- [x] `proxy.linshisan.cc:8443` VPN 正常
- [x] `www.linshisan.cc` 经 Cloudflare 正常
- [x] Cloudflare 已切换 Full (strict)
- [ ] 审计日志显示真实客户端 IP
- [ ] 自动封禁使用真实 IP并完成飞书通知测试
- [ ] 会话绑定重新开启后不再误退出
- [ ] 攻击 IP没有新的登录调用记录

## 7. 回滚

### 7.1 Full (strict) 切换失败

立即在 Cloudflare 将 SSL/TLS 加密模式恢复为 Flexible，并确认：

```bash
curl -fsSI https://www.linshisan.cc
sudo journalctl -u caddy -n 100 --no-pager
```

该回滚只改变 Cloudflare 到源站的回源协议，不需要回滚 Caddy、sub2api 回环绑定或 8443 VPN。定位并修复证书、Caddy 443、UFW 443 或其他橙云源站问题后，再重新执行步骤 4 的切换闸门。

### 7.2 Caddy或端口切换失败

仅在紧急恢复服务时使用以下回滚；它会重新暴露原漏洞：

```bash
sudo systemctl stop caddy
cd ~/docker/sub2api
cp docker-compose.yml.before-caddy docker-compose.yml
docker compose up -d --no-deps --force-recreate sub2api
```

恢复后应尽快重新处理，不能把 `0.0.0.0:80->8080` 作为长期状态。

### 7.3 真实 IP识别异常

关闭统一转发 IP信任：

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "UPDATE settings SET value='false' WHERE key='api_key_acl_trust_forwarded_ip';"
```

同时保持会话绑定和自动封禁关闭，排查 Caddy 转发头与 Cloudflare来源限制。

### 7.4 自动封禁误封

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "UPDATE settings SET value='false' WHERE key='feishu_login_bruteforce_autoban_enabled';"
```

已存在的封禁不会因关闭开关自动消失，需要等待 TTL 到期或按自动封禁文档手动解封。

### 7.5 会话绑定异常

```bash
docker compose exec postgres psql -U sub2api -d sub2api \
  -c "UPDATE settings SET value='false' WHERE key='session_binding_enabled';"
```

## 8. 已废弃方案

“只调整 UFW 80 端口规则、不改变 Docker 公网映射”的方案已经通过实测证明无效，不得再次执行。UFW 的 Cloudflare-only 规则只有在宿主机 Caddy直接监听 80/443、sub2api 退出 Docker 公网发布后才会真正生效。
