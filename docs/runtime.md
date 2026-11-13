# 运行模式：裸机 / Docker

面板有两种运行模式，由环境变量 `SANDBOX_RUNTIME`（兼容旧名 `RUNTIME`）控制：

| 取值 | 行为 |
| --- | --- |
| `local` | sing-box 以**宿主机进程**方式运行，不需要 Docker |
| `docker` | 每个 sing-box 实例是一个容器（上游原有行为） |
| 未设置 | auto：`/var/run/docker.sock` 存在则用 `docker`，否则用 `local` |

两种模式共用同一套 HTTP 接口与前端，切换只改环境变量。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `SANDBOX_RUNTIME` | auto | `local` / `docker` |
| `SINGBOX_BIN` | 自动探测 | local 模式使用的 sing-box 二进制路径，依次探测 `/usr/local/bin/sing-box`、`/usr/bin/sing-box`、`/opt/singbox/sing-box` |
| `DATA_DIR` | 工作目录 | 数据根目录 |
| `HOST_DATA_DIR` | — | 仅 docker 模式，容器挂载所需的宿主路径 |
| `LISTEN_ADDR` | `127.0.0.1:7000` | 面板监听地址 |

## 目录布局（local 模式）

```
$DATA_DIR/
├── singbox/
│   ├── config.json                 默认实例配置
│   └── configs/<name>/config.json  命名实例配置
├── run/<name>.pid                  进程号
├── logs/<name>.log                 实例日志（超过 8MB 自动轮转为 .log.1）
└── state/                          sing-box -D 工作目录（缓存、ACME 等）
```

实例名 `default` 对应默认实例，其余对应 `configs/<name>`。

## 裸机安装

```bash
# 1. 构建面板
cd server && go build -o ../singbox-ui .
cd .. && sudo ./deploy/install.sh
```

脚本会完成：安装 `nftables`/`iproute` 依赖 → 下载 sing-box 到 `/usr/local/bin` →
安装面板到 `/opt/singbox-ui/bin` → 写 `/etc/singbox-ui/env` →
注册 `singbox-ui.service` 并启动 → 健康检查。

自定义监听地址：

```bash
sudo LISTEN_ADDR=0.0.0.0:7000 ./deploy/install.sh
```

指定 sing-box 版本：`sudo SB_VER=1.13.5 ./deploy/install.sh`。

服务管理：

```bash
systemctl status singbox-ui
journalctl -u singbox-ui -f
systemctl restart singbox-ui
```

卸载：

```bash
systemctl disable --now singbox-ui
rm -rf /opt/singbox-ui /etc/singbox-ui /etc/systemd/system/singbox-ui.service
```

## Docker 部署

```bash
docker compose up -d --build
```

`docker-compose.yml` 已显式设置 `SANDBOX_RUNTIME=docker`，并挂载
`/var/run/docker.sock`、使用 `network_mode: host` + `NET_ADMIN`，
这样实例容器与网关规则都直接作用在宿主网络。

## 实例生命周期

- **启动**：`POST /api/singbox/run`，子进程经 `setsid` 脱离面板进程组，
  面板重启或崩溃都不会中断 sing-box。
- **接管**：PID 写在 `$DATA_DIR/run/<name>.pid`，面板重启后按 PID + `/proc/<pid>/cmdline`
  双重校验识别既有实例（防止 PID 复用误判）。
- **停止**：`SIGTERM` → 最多等 8 秒 → `SIGKILL`，随后清理 PID 文件。
- **日志**：子进程 stdout/stderr 追加到 `$DATA_DIR/logs/<name>.log`，
  API 返回前会剥离 ANSI 颜色码。
- **僵尸**：启动后由独立 goroutine `wait()` 回收，不会留下 `<defunct>`。

## 兼容性处理

sing-box 1.12+ 对缺少 `route.default_domain_resolver` 的配置直接 FATAL。
为兼容存量配置，两种模式启动实例时都会注入：

```
ENABLE_DEPRECATED_MISSING_DOMAIN_RESOLVER=true
```

同时面板生成的默认配置已显式带上 `route.default_domain_resolver`。

## 已知限制

- **测速功能**（`/api/speedtest/*`）依赖临时容器，目前仅 `docker` 模式可用。
- 机器重启后实例不会自动拉起，需要在面板上重新启动（PID 文件已在重启后失效并自动清理）。
- 面板本身无鉴权，`LISTEN_ADDR` 设为 `0.0.0.0` 时请只在内网使用或加防火墙限制。
