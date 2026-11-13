#!/usr/bin/env bash
# sing-box 网关管理平台 —— 裸机（systemd）一键安装
# 适用：Debian 12 / Ubuntu 22+ / AlmaLinux 9 / Rocky 9 等主流 Linux
# 用法：sudo ./install.sh
#       sudo LISTEN_ADDR=0.0.0.0:7000 ./install.sh
set -euo pipefail

PREFIX=/opt/singbox-ui
DATA=$PREFIX/data
BIN=$PREFIX/bin/singbox-ui
ENVF=/etc/singbox-ui/env
UNIT=/etc/systemd/system/singbox-ui.service
SB_BIN=/usr/local/bin/sing-box
SB_VER=${SB_VER:-1.13.5}
LISTEN=${LISTEN_ADDR:-0.0.0.0:7000}

c() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31m!!\033[0m %s\n' "$*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || die "请用 root 运行（需要写 systemd 与 /usr/local/bin）"

# ---------- 1. 依赖 ----------
c "检查依赖"
need=""
for b in nft ip curl tar; do command -v "$b" >/dev/null 2>&1 || need="$need $b"; done
if [[ -n $need ]]; then
  if command -v dnf >/dev/null 2>&1; then
    dnf install -y nftables iproute curl tar >/dev/null
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y nftables iproute2 curl tar >/dev/null
  else
    die "未识别的包管理器，请手动安装：$need"
  fi
fi

# ---------- 2. sing-box ----------
if [[ -x $SB_BIN ]]; then
  c "已存在 sing-box，跳过安装"
else
  case $(uname -m) in
    x86_64)  a=amd64 ;;
    aarch64) a=arm64 ;;
    armv7l)  a=armv7 ;;
    *) die "不支持的架构: $(uname -m)" ;;
  esac
  c "下载 sing-box v$SB_VER ($a)"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL -o "$tmp/sb.tgz" \
    "https://github.com/SagerNet/sing-box/releases/download/v${SB_VER}/sing-box-${SB_VER}-linux-${a}.tar.gz" \
    || die "下载失败，请检查网络或手动放置二进制到 $SB_BIN"
  tar xzf "$tmp/sb.tgz" -C "$tmp"
  install -m 0755 "$tmp/sing-box-${SB_VER}-linux-${a}/sing-box" "$SB_BIN"
fi
c "sing-box: $($SB_BIN version | head -1)"

# ---------- 3. 面板二进制 ----------
mkdir -p "$PREFIX/bin" "$DATA" /etc/singbox-ui
if [[ -x ./singbox-ui ]]; then
  install -m 0755 ./singbox-ui "$BIN"
  c "已安装面板二进制"
elif [[ -x $BIN ]]; then
  c "使用已有 $BIN"
else
  die "缺少面板二进制。请先在 server 目录构建：go build -o ../singbox-ui . 然后重跑本脚本"
fi

# ---------- 4. 环境变量 ----------
cat > "$ENVF" <<EOF
DATA_DIR=$DATA
SANDBOX_RUNTIME=local
SINGBOX_BIN=$SB_BIN
LISTEN_ADDR=$LISTEN
TZ=Asia/Shanghai
EOF
chmod 600 "$ENVF"

# ---------- 5. systemd ----------
cat > "$UNIT" <<'EOF'
[Unit]
Description=sing-box web panel (gateway / multi-instance)
Documentation=https://github.com/bi4nbn/singbox_ui
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/singbox-ui/env
ExecStart=/opt/singbox-ui/bin/singbox-ui
Restart=always
RestartSec=3
LimitNOFILE=65535
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
EOF

# AlmaLinux / RHEL 默认 SELinux enforcing，需要给 /opt 下的可执行文件打标签
if command -v getenforce >/dev/null 2>&1 && [[ $(getenforce) == Enforcing ]]; then
  chcon -R -t bin_t "$PREFIX/bin" 2>/dev/null || true
  c "已为 SELinux 打 bin_t 标签"
fi

systemctl daemon-reload
systemctl enable singbox-ui >/dev/null 2>&1
systemctl restart singbox-ui

# ---------- 6. 防火墙放行（仅当监听 0.0.0.0） ----------
if [[ $LISTEN == 0.0.0.0* ]]; then
  p=${LISTEN##*:}
  if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="$p/tcp" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    c "firewalld 已放行 $p/tcp"
  elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi "Status: active"; then
    ufw allow "$p/tcp" >/dev/null 2>&1 || true
    c "ufw 已放行 $p/tcp"
  else
    c "未检测到启用的防火墙，跳过放行"
  fi
fi

# ---------- 7. 健康检查 ----------
sleep 3
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
port=${LISTEN##*:}
if curl -fsS -m 5 "http://127.0.0.1:${port}/api/singbox/version" >/dev/null 2>&1; then
  c "面板已就绪： http://${ip}:${port}"
else
  journalctl -u singbox-ui -n 15 --no-pager
  die "健康检查失败，请查看上面的日志"
fi

cat <<EOF

下一步：
  1) 首次使用先 POST /api/singbox/ensure-image 检查 sing-box，再 POST /api/singbox/run 启动实例
  2) 网关模式（tproxy）在面板「网关模式」页开启，需要本机 nftables 可用
  3) 面板目前无鉴权，LISTEN_ADDR 若为 0.0.0.0 请只在内网使用或用防火墙限制来源
  4) 卸载：systemctl disable --now singbox-ui && rm -rf $PREFIX /etc/singbox-ui $UNIT
EOF
