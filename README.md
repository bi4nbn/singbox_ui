# Sing-box UI

**[English](README_EN.md) | 中文**

<div align="center">

[![Docker Image](https://img.shields.io/badge/ghcr.io-singbox__ui-blue?logo=docker)](https://github.com/SpadesA99/singbox_ui/pkgs/container/singbox_ui)
[![Build Status](https://github.com/SpadesA99/singbox_ui/actions/workflows/docker-build.yml/badge.svg)](https://github.com/SpadesA99/singbox_ui/actions)
[![GitHub Stars](https://img.shields.io/github/stars/SpadesA99/singbox_ui?style=flat&logo=github)](https://github.com/SpadesA99/singbox_ui/stargazers)
[![License](https://img.shields.io/github/license/SpadesA99/singbox_ui)](LICENSE)

**现代化的 sing-box 配置管理工具**

基于 Go 1.24 + Next.js 16 构建，通过 Docker 容器化管理 sing-box

</div>

![Sing-box UI 界面截图](docs/zh.png)

---

## 核心功能

### 协议支持

| 入站协议 | 出站协议 | 订阅解析 |
|---------|---------|---------|
| WireGuard | 所有入站协议 | VMess |
| Mixed (Socks5+HTTP) | direct (直连) | VLESS |
| VLESS | block (屏蔽) | Trojan |
| VMess | | Shadowsocks |
| Trojan | | AnyTLS |
| Shadowsocks | | Clash YAML 格式 |
| Hysteria2 | | |
| TUIC | | |
| Naive | | |
| ShadowTLS | | |
| AnyTLS | | |
| HTTP | | |

### TLS 证书管理

- **ACME 自动证书**: 支持 Let's Encrypt 自动申请和续期
- **手动证书**: 支持上传自有证书文件
- **多协议支持**: VLESS、VMess、Trojan、Hysteria2 等 TLS 协议均支持 ACME

### 路由规则配置

- **快速模板**: 一键启用常用规则（广告屏蔽、中国 IP/域名直连、私有 IP 直连）
- **快速添加规则**: 支持快速添加 IP 或域名到直连/代理/屏蔽列表
- **直连模式**: 无代理出站时自动配置为直连模式

### 负载均衡

- **URLTest 模式**: 基于 sing-box `urltest` 出站，自动选择最低延迟节点
- **可配置容差**: 自定义延迟容差值（默认 50ms），避免频繁切换
- **动态节点池**: 从订阅中灵活选择多个节点组成负载均衡组
- **智能路由**: 自动生成路由规则，实现流量智能分发

### 多实例管理

- 支持创建多个命名 sing-box 实例
- 每个实例独立配置、启停
- 独立的容器日志和状态监控

### WireGuard VPN 管理

- Curve25519 密钥生成
- IP 绑定密钥缓存
- 客户端配置管理 (批量生成、二维码、配置下载)

### Cloudflare WARP 出站

- **一键注册**: 自动生成 Curve25519 密钥对并调用 Cloudflare 注册 API, 本地缓存设备 token, 无需手动配置
- **WARP+ 绑定**: 支持填入 License 绑定 WARP+ 账户, 获得无限流量
- **端点优选**: 真实 WireGuard 握手探测——向候选 `IP:Port` 发送 WG 初始化包, 校验返回的 92 字节 `MessageResponse`, 按丢包率 + 平均 RTT 排序, 精确反映 UDP 路径质量(参考 [CloudflareWarpSpeedTest](https://github.com/peanut996/CloudflareWarpSpeedTest) 实现)
- **广覆盖扫描**: 8 个 Cloudflare /24 段 × 54 个已知 WARP UDP 端口, 并发探测并打乱采样, 快速找到最快的边缘节点

### 节点健康探测

- 异步多节点并发探测
- 成功率滑动窗口统计
- API 轮询获取探测结果

### 管理功能

- 配置预览 (JSON 编辑器)
- 容器日志查看
- 容器状态监控

### 运行模式
- **裸机优先**: 无 Docker 环境下以宿主机进程管理 sing-box 实例，PID / 日志 / 起停全托管
- **Docker 兼容**: 同一套接口，`SANDBOX_RUNTIME=docker` 即可回到容器方式
- 面板重启不中断实例（`setsid` 脱离 + PID 文件接管），停止时 `SIGTERM` 优雅退出
- 详见 [docs/runtime.md](docs/runtime.md)

### 网关模式 (tproxy)
- **一键接管**: 独立 nft 表 + ip rule + local 路由表，把局域网流量交给 sing-box tproxy
- **三要素自愈**: nft 表 / ip rule / ip route 任一丢失自动重建（实测 10~25s 恢复）
- **接口可选**: 默认自动探测默认路由接口，也支持手工指定
- **不破坏环境**: 绝不 flush ruleset，只维护自有表，卸载即删自身资源
- 详见 [docs/gateway.md](docs/gateway.md)

---

## 快速开始

### 裸机安装（首选）

不需要 Docker，sing-box 直接以宿主机进程运行，面板用 systemd 托管。

```bash
# 构建面板（需要 Go 1.24+）
cd server && go build -o ../singbox-ui .
cd .. && sudo ./deploy/install.sh
```

脚本依次完成：安装 `nftables`/`iproute` → 下载 sing-box 到 `/usr/local/bin` →
安装面板到 `/opt/singbox-ui/bin` → 写 `/etc/singbox-ui/env` →
注册并启动 `singbox-ui.service` → 健康检查并打印访问地址。

自定义监听地址：`sudo LISTEN_ADDR=0.0.0.0:7000 ./deploy/install.sh`

运行模式由 `SANDBOX_RUNTIME` 决定：`local` 走宿主机进程，`docker` 走容器；
不设置时自动探测（有 `docker.sock` 用 docker，否则用 local）。
目录布局、实例生命周期与限制见 [docs/runtime.md](docs/runtime.md)。

### Docker Compose（兼容路径）

创建 `docker-compose.yml`:

```yaml
services:
  singbox-ui:
    image: ghcr.io/spadesa99/singbox_ui:latest
    container_name: singbox-ui
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/home/data
    environment:
      - DATA_DIR=/home/data
      - HOST_DATA_DIR=${PWD}/data
      - LISTEN_ADDR=127.0.0.1:7000
      - SANDBOX_RUNTIME=docker
      - TZ=Asia/Shanghai
```

```bash
docker compose up -d
```

访问 http://127.0.0.1:7000

> **说明**:
> - 使用 `network_mode: host` 以便容器直接使用宿主机网络
> - 默认监听 `127.0.0.1:7000`，仅本地访问，可通过 `LISTEN_ADDR` 环境变量自定义
> - 挂载 Docker Socket 用于管理 sing-box 容器
> - `HOST_DATA_DIR` 通过 `${PWD}` 自动映射宿主机数据目录，用于 sing-box 容器挂载
> - 内置 sing-box v1.13.5 镜像，首次启动自动加载，无需联网拉取

### 远程访问

服务默认仅监听 `127.0.0.1`，推荐通过 SSH 隧道安全访问：

```bash
ssh -L 7000:127.0.0.1:7000 user@your-server
```

然后在本地浏览器访问 http://127.0.0.1:7000

> **安全提示**: 不建议将 `LISTEN_ADDR` 改为 `0.0.0.0:7000` 直接暴露到公网，管理面板无认证保护。如需外网访问，请使用 SSH 隧道或配置带认证的反向代理（如 Nginx + Basic Auth）。

---

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `DATA_DIR` | 容器内数据目录 | `/home/data` |
| `HOST_DATA_DIR` | 宿主机数据目录（用于 sing-box 容器挂载） | `${PWD}/data` |
| `LISTEN_ADDR` | 服务监听地址 | `127.0.0.1:7000` |
| `SANDBOX_RUNTIME` | 运行模式：`local` 宿主进程 / `docker` 容器 / 留空自动探测 | 自动 |
| `SINGBOX_BIN` | local 模式下的 sing-box 二进制路径 | 自动探测 |
| `TZ` | 时区 | `Asia/Shanghai` |

---

## 技术栈

| 前端 | 后端 |
|------|------|
| Next.js 16 | Go 1.24 |
| React 19 | Gin 1.11 |
| Tailwind CSS | Docker SDK |
| shadcn/ui | |

---

## 许可证

[MIT License](LICENSE)

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=SpadesA99/singbox_ui&type=Date)](https://star-history.com/#SpadesA99/singbox_ui&Date)

## 致谢

- [sing-box](https://github.com/SagerNet/sing-box)
- [Next.js](https://nextjs.org/)
- [Gin](https://github.com/gin-gonic/gin)
- [shadcn/ui](https://ui.shadcn.com/)
