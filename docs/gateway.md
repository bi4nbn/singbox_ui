# 网关模式（tproxy）

把本机变成透明代理网关：局域网设备只要把网关/DNS 指到这台机器，流量就会被 nft 打标并投递给 sing-box 的 tproxy 入站。

本功能是在 sing-box UI 原有「配置管理 + 多实例」能力之上补的一层**网络编排**，不改动 sing-box 本身。

## 前置要求

| 依赖 | 说明 |
| --- | --- |
| Linux 内核 ≥ 4.18 | 需要 `nftables` 与 `tproxy` |
| `nft` 命令 | Debian/Ubuntu：`apt install nftables`；Alpine：`apk add nftables` |
| `ip` 命令 | iproute2，几乎所有发行版自带 |
| `NET_ADMIN` | nft / ip rule / ip route 都需要 |
| 宿主 netns | Docker 必须 `network_mode: host`，否则容器里的 nft 只改到容器自己的命名空间 |

## 工作原理：三要素缺一不可

tproxy 不是一条规则就能生效，必须同时具备：

1. **nft 规则**：在 `prerouting` 打 mark 并把连接交给 tproxy
   ```
   meta l4proto { tcp, udp } ct direction original meta mark set 0x1 tproxy ip to :9888 accept
   ```
2. **ip rule**：让带 mark 的包走专用路由表
   ```
   ip rule add fwmark 0x1 lookup 100
   ```
3. **ip route**：该表里把目的地址判为本地，包才会交给本地 socket
   ```
   ip route add local 0.0.0.0/0 dev lo table 100
   ```

本项目的自愈循环每 `heal_interval` 秒检查这三项，任一缺失即重建。实测：删掉 nft 表 10 秒内恢复，删掉 ip rule 25 秒内恢复（`heal_count` 递增可核对）。

## 与 smbox 的差异（刻意规避的坑）

| smbox 的做法 | 本项目 |
| --- | --- |
| 写 `/etc/nftables.conf`，含 `flush ruleset` | 只维护独立表 `table ip smbox`，从不 `flush ruleset`，不碰 firewalld/其他表 |
| 接口白名单由程序自动算，无法干预 | 默认自动探测默认路由接口，**也允许手工指定** |
| 表被外部重写后规则消失 | 后台自愈循环 + 立即自愈接口 |
| tproxy 规则散落在主配置 | 独立表 + 独立 ip rule/table，卸载即 `nft delete table` |

## 使用

1. 打开面板 → 左侧「网关模式」
2. 选择出口接口（默认「自动（默认路由接口）」）
3. 填局域网网段（用于 masquerade，例如 `192.168.80.0/24`）
4. 点「应用并启用」
5. 若提示 `restart_required`，说明已向默认实例的 `config.json` 注入
   ```json
   { "type": "tproxy", "tag": "tproxy-in", "listen": "0.0.0.0", "listen_port": 9888, "network": "tcp,udp" }
   ```
   到「入站」页重启实例即可生效（原配置已备份为 `config.json.bak`）

### 客户端侧

- 默认网关指向本机
- DNS 指向本机的 DNS 入站端口（默认 5353），或直接用本机 53（若你另外做了转发）
- 不需要在客户端装任何软件

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/gateway/status` | 三要素状态、接口、上次应用时间、自愈次数、当前 nft 全文 |
| GET | `/api/gateway/config` | 当前网关配置 |
| POST | `/api/gateway/config` | 只保存不应用 |
| GET | `/api/gateway/interfaces` | 接口列表（标记默认路由所在者） |
| GET | `/api/gateway/rules` | 预览将加载的 nft 文本 |
| POST | `/api/gateway/apply` | 保存并应用（可选 body 覆盖配置），必要时注入 tproxy 入站 |
| POST | `/api/gateway/stop` | 停用并清理表/rule/route |
| POST | `/api/gateway/heal` | 立即执行一次自愈 |

状态返回示例：

```json
{
  "enabled": true,
  "interface": "ens34",
  "auto_detect": false,
  "table_name": "ip smbox",
  "nft_ok": true,
  "ip_rule_ok": true,
  "ip_route_ok": true,
  "healthy": true,
  "heal_count": 2,
  "marker": "0x1",
  "rule_table": 100,
  "tproxy_port": 9888
}
```

## 排错

```bash
# 三要素逐个看
nft list table ip smbox
ip rule show | grep fwmark
ip route show table 100

# 谁在占 tproxy 端口
ss -lntup | grep 9888

# 看服务日志
docker logs -f singbox-ui
```

常见原因：

- **客户端连不上 → 网关模式显示健康**：sing-box 实例没重启，tproxy 入站没起来（`ss -lntup | grep 9888` 为空）
- **只有本机能上网**：客户端网关/DNS 没指到本机，或客户端所在网段不在 `lan_cidr` 里导致回程没有 masquerade
- **nft 表反复消失**：有别的程序在 `flush ruleset`，查看 `/etc/nftables.conf` 或 firewalld 的 reload 行为；本项目自愈能覆盖，但建议找出源头

## 卸载

面板点「停用并清理」即可，等价于：

```bash
nft delete table ip smbox
ip rule del fwmark 0x1 lookup 100
ip route del local 0.0.0.0/0 dev lo table 100
```

`config.json.bak` 可用来回滚被注入的 tproxy 入站。
