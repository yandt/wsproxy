#!/usr/bin/env bash
# 在 Linux 上安装 wsproxy，或从本机 SSH 装到远端。
# 能问答填配置，也能事后改：sudo ./install.sh config
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
HTTP="${HTTP:-:8080}"
SSH_ADDR="${SSH_ADDR:-:2222}"
AGENT_TOKEN="${AGENT_TOKEN:-}"
ACCESS_TOKEN="${ACCESS_TOKEN:-}"
SERVER_URL="${SERVER_URL:-}"
CLIENT_ID="${CLIENT_ID:-}"
RUN_USER="${RUN_USER:-}"
BIN="${BIN:-}"
GITHUB_REPO="${GITHUB_REPO:-yandt/wsproxy}"
WSPROXY_VERSION="${WSPROXY_VERSION:-latest}"
FROM_SOURCE=0
FORCE=0
NO_SYSTEMD=0
YES=0
ASK=0
EXPOSES=()
ALLOW_IPS=()
ALLOW_CLIENTS=()
ALLOW_TARGETS=()

CONF_DIR=/etc/wsproxy
DATA_DIR=/var/lib/wsproxy
SERVER_CONF="$CONF_DIR/server.yaml"
CLIENT_CONF="$CONF_DIR/client.yaml"

usage() {
  cat <<'EOF'
用法:
  Linux 上一句装最新版（会出菜单，一项项问）:
    curl -fsSL https://github.com/yandt/wsproxy/releases/latest/download/install.sh | sudo bash

  直接装服务端 / 客户端 / 只更新程序:
    curl -fsSL https://github.com/yandt/wsproxy/releases/latest/download/install.sh | sudo bash -s -- server
    curl -fsSL https://github.com/yandt/wsproxy/releases/latest/download/install.sh | sudo bash -s -- client
    curl -fsSL https://github.com/yandt/wsproxy/releases/latest/download/install.sh | sudo bash -s -- upgrade

  装到当前这台 Linux（不写参数会出菜单）:
    sudo ./install.sh
    sudo ./install.sh server
    sudo ./install.sh client

  事后改配置（读出现有值，改完自动重启）:
    sudo ./install.sh config

  从本机仓库装到远端 Linux（会交叉编译再 scp，远端同样能问答）:
    ./install.sh remote 用户@主机
    ./install.sh remote 用户@主机 server
    ./install.sh remote 用户@主机 client
    ./install.sh remote 用户@主机 config

  没有 systemd 的机器（容器、部分开发环境）:
    安装时会自动改用后台进程，不依赖 systemctl。
    sudo ./install.sh start client
    sudo ./install.sh stop client
    sudo ./install.sh status client
    也可前台跑: wsproxy client --config /etc/wsproxy/client.yaml

  卸掉:
    sudo ./install.sh uninstall

服务端选项:
  --http :8080            网页和客户端连入端口
  --ssh :2222             SSH 入口
  --agent-token TOKEN     客户端连服务器的口令
  --access-token TOKEN    外人访问用的 token
  --allow-ip 地址         可重复。谁能连网页/SSH/隧道入口
  --allow-client 名字     可重复。允许上线的客户端
  --allow-target 地址     可重复。隧道允许转到哪里
  --user 用户             跑服务的系统用户（默认 wsproxy）

客户端选项:
  --server ws://主机:8080
  --agent-token TOKEN
  --id 名字               不写用主机名
  --expose 规则           可重复
  --allow-target 地址     可重复。本机允许连出到哪里
  --user 用户             跑客户端的用户（默认 sudo 之前那个用户）

共用:
  --bin 路径              用现成的 wsproxy 二进制
  --release 版本          下载指定发行版，默认 latest
  --from-source           不下载，用本仓库现场编译
  --prefix /usr/local
  --force                 已有配置也覆盖
  --ask                   即使写了参数也再问一遍
  --yes                   不提问，缺的口令自动生成
  --no-systemd            只拷文件，不装开机自启
EOF
}

die() {
  echo "错误: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令: $1"
}

have_tty() {
  [[ -r /dev/tty && -w /dev/tty ]]
}

want_ask() {
  [[ "$YES" -eq 0 ]] && { [[ "$ASK" -eq 1 ]] || have_tty; }
}

yaml_str() {
  local s=$1
  s=${s//\'/\'\'}
  printf "'%s'" "$s"
}

rand_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 16
    return
  fi
  dd if=/dev/urandom bs=16 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n'
}

is_root() {
  [[ "$(id -u)" -eq 0 ]]
}

here_dir() {
  local src=${BASH_SOURCE[0]:-$0}
  [[ -f "$src" ]] || { echo ""; return; }
  cd "$(dirname "$src")" && pwd
}

current_os() {
  case "$(uname -s)" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    *) die "现在只支持 Linux / macOS，当前是 $(uname -s)" ;;
  esac
}

release_url() {
  local name=$1
  local ver=$WSPROXY_VERSION
  if [[ "$ver" == latest || -z "$ver" ]]; then
    echo "https://github.com/${GITHUB_REPO}/releases/latest/download/${name}"
    return
  fi
  [[ "$ver" == v* ]] || ver="v$ver"
  echo "https://github.com/${GITHUB_REPO}/releases/download/${ver}/${name}"
}

download_file() {
  local url=$1 dest=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 20 -o "$dest" "$url"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -O "$dest" "$url"
    return
  fi
  die "需要 curl 或 wget 才能下载发行包"
}

download_release() {
  local dest=$1
  local os arch name url tmp
  os=$(current_os)
  arch=$(map_arch "$(uname -m)")
  name="wsproxy-${os}-${arch}.tar.gz"
  url=$(release_url "$name")
  tmp=$(mktemp -d)
  echo "正在下载 ${WSPROXY_VERSION} ${os}/${arch}"
  echo "  $url"
  if ! download_file "$url" "$tmp/$name"; then
    rm -rf "$tmp"
    return 1
  fi
  tar -xzf "$tmp/$name" -C "$tmp"
  [[ -f "$tmp/wsproxy" ]] || { rm -rf "$tmp"; die "发行包装得不对，里面没有 wsproxy"; }
  install -m 0755 "$tmp/wsproxy" "$dest"
  rm -rf "$tmp"
}

map_arch() {
  case "$1" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    armv7l | armv7) echo arm ;;
    *) die "不支持的 CPU: $1" ;;
  esac
}

norm_listen() {
  local v=$1
  [[ -z "$v" ]] && { echo "$2"; return; }
  [[ "$v" == *:* ]] && { echo "$v"; return; }
  echo ":$v"
}

norm_server() {
  local v=$1
  [[ -z "$v" ]] && { echo ""; return; }
  case "$v" in
    ws://* | wss://* | http://* | https://*) echo "$v" ;;
    *) echo "ws://$v" ;;
  esac
}

ask() {
  local dest=$1 prompt=$2 default=${3:-} _ans=""
  if [[ -n "$default" ]]; then
    printf '%s [%s]: ' "$prompt" "$default" >/dev/tty
  else
    printf '%s: ' "$prompt" >/dev/tty
  fi
  IFS= read -r _ans </dev/tty || true
  [[ -z "$_ans" ]] && _ans=$default
  printf -v "$dest" '%s' "$_ans"
}

ask_required() {
  local dest=$1 prompt=$2 default=${3:-} cur
  while true; do
    ask "$dest" "$prompt" "$default"
    eval "cur=\$$dest"
    if [[ -n "$cur" ]]; then
      return
    fi
    echo "这项必填。" >/dev/tty
  done
}

yesno() {
  local _yn=""
  ask _yn "$1" "${2:-n}"
  case "$_yn" in
    y | Y | yes | Yes | 是 | 好) return 0 ;;
    *) return 1 ;;
  esac
}

join_csv() {
  local IFS=','
  echo "$*"
}

csv_to_array() {
  local dest=$1 raw=$2
  local -a items=()
  local one tmp
  IFS=',' read -ra tmp <<<"$raw"
  for one in "${tmp[@]}"; do
    one="${one#"${one%%[![:space:]]*}"}"
    one="${one%"${one##*[![:space:]]}"}"
    [[ -n "$one" ]] && items+=("$one")
  done
  eval "$dest=(\"\${items[@]}\")"
}

yaml_get() {
  local file=$1 key=$2
  [[ -f "$file" ]] || return 0
  awk -v key="$key" '
    $0 ~ "^" key ":" {
      sub("^[^:]+:[ \t]*", "")
      gsub(/\r$/, "")
      gsub(/^'\''/, "")
      gsub(/'\''$/, "")
      gsub(/^"/, "")
      gsub(/"$/, "")
      print
      exit
    }
  ' "$file"
}

yaml_list() {
  local file=$1 key=$2
  [[ -f "$file" ]] || return 0
  awk -v key="$key" '
    $0 ~ "^" key ":" { grab=1; next }
    grab && /^[^ \t#-]/ { exit }
    grab && /^[ \t]*-[ \t]*/ {
      sub(/^[ \t]*-[ \t]*/, "")
      gsub(/\r$/, "")
      gsub(/^'\''/, "")
      gsub(/'\''$/, "")
      gsub(/^"/, "")
      gsub(/"$/, "")
      if ($0 != "") print
    }
  ' "$file"
}

write_yaml_list() {
  local key=$1
  shift
  [[ $# -eq 0 ]] && return
  echo "$key:"
  local item
  for item in "$@"; do
    echo "  - $(yaml_str "$item")"
  done
}

load_server_file() {
  [[ -f "$SERVER_CONF" ]] || return 0
  HTTP=$(yaml_get "$SERVER_CONF" http)
  SSH_ADDR=$(yaml_get "$SERVER_CONF" ssh)
  AGENT_TOKEN=$(yaml_get "$SERVER_CONF" agent_token)
  ACCESS_TOKEN=$(yaml_get "$SERVER_CONF" access_token)
  [[ -n "$HTTP" ]] || HTTP=:8080
  [[ -n "$SSH_ADDR" ]] || SSH_ADDR=:2222
  mapfile -t ALLOW_IPS < <(yaml_list "$SERVER_CONF" allow_ips)
  mapfile -t ALLOW_CLIENTS < <(yaml_list "$SERVER_CONF" allow_clients)
  mapfile -t ALLOW_TARGETS < <(yaml_list "$SERVER_CONF" allow_targets)
}

load_client_file() {
  [[ -f "$CLIENT_CONF" ]] || return 0
  SERVER_URL=$(yaml_get "$CLIENT_CONF" server)
  AGENT_TOKEN=$(yaml_get "$CLIENT_CONF" agent_token)
  CLIENT_ID=$(yaml_get "$CLIENT_CONF" id)
  mapfile -t EXPOSES < <(yaml_list "$CLIENT_CONF" expose)
  mapfile -t ALLOW_TARGETS < <(yaml_list "$CLIENT_CONF" allow_targets)
}

parse_args() {
  ROLE=${1:-}
  [[ $# -gt 0 ]] && shift
  case "$ROLE" in
    "" | server | client | uninstall | config | upgrade | start | stop | status) ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage
      die "第一个参数应是 server、client、config、upgrade、start、stop、status 或 uninstall"
      ;;
  esac

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --http)
        HTTP=$2
        shift 2
        ;;
      --ssh)
        SSH_ADDR=$2
        shift 2
        ;;
      --agent-token)
        AGENT_TOKEN=$2
        shift 2
        ;;
      --access-token)
        ACCESS_TOKEN=$2
        shift 2
        ;;
      --server)
        SERVER_URL=$2
        shift 2
        ;;
      --id)
        CLIENT_ID=$2
        shift 2
        ;;
      --user)
        RUN_USER=$2
        shift 2
        ;;
      --bin)
        BIN=$2
        shift 2
        ;;
      --release)
        WSPROXY_VERSION=$2
        shift 2
        ;;
      --from-source)
        FROM_SOURCE=1
        shift
        ;;
      --prefix)
        PREFIX=$2
        shift 2
        ;;
      --expose)
        EXPOSES+=("$2")
        shift 2
        ;;
      --allow-ip)
        ALLOW_IPS+=("$2")
        shift 2
        ;;
      --allow-client)
        ALLOW_CLIENTS+=("$2")
        shift 2
        ;;
      --allow-target)
        ALLOW_TARGETS+=("$2")
        shift 2
        ;;
      --force)
        FORCE=1
        shift
        ;;
      --ask)
        ASK=1
        shift
        ;;
      --yes)
        YES=1
        shift
        ;;
      --no-systemd)
        NO_SYSTEMD=1
        shift
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        die "未知参数: $1"
        ;;
    esac
  done
}

prompt_menu() {
  echo >/dev/tty
  echo "wsproxy 安装 / 配置" >/dev/tty
  echo "  1) 装服务端" >/dev/tty
  echo "  2) 装客户端" >/dev/tty
  echo "  3) 改现有配置" >/dev/tty
  echo "  4) 只更新到最新版程序" >/dev/tty
  echo "  5) 卸载" >/dev/tty
  echo >/dev/tty
  local choice=""
  ask choice "选一项" "1"
  case "$choice" in
    1 | server) ROLE=server ;;
    2 | client) ROLE=client ;;
    3 | config) ROLE=config ;;
    4 | upgrade) ROLE=upgrade ;;
    5 | uninstall) ROLE=uninstall ;;
    *) die "无法识别: $choice" ;;
  esac
}

prompt_server() {
  echo >/dev/tty
  echo "—— 服务端配置（直接回车用括号里的值）——" >/dev/tty
  local v
  ask v "网页/客户端连入口（只写数字也行）" "${HTTP#:}"
  HTTP=$(norm_listen "$v" ":8080")
  ask v "SSH 入口" "${SSH_ADDR#:}"
  SSH_ADDR=$(norm_listen "$v" ":2222")
  ask AGENT_TOKEN "隧道口令（客户端连服务器用，回车则自动生成）" "$AGENT_TOKEN"
  ask ACCESS_TOKEN "访问 token（外人 SSH/网页用，回车则自动生成）" "$ACCESS_TOKEN"
  ask RUN_USER "跑服务的系统用户" "${RUN_USER:-wsproxy}"

  if yesno "要设来源 IP 白名单吗？（不设则谁都能连网页/SSH）" n; then
    ask v "允许的 IP/网段，逗号分隔" "$(join_csv "${ALLOW_IPS[@]}")"
    csv_to_array ALLOW_IPS "$v"
  fi
  if yesno "要限制哪些客户端名字能上线吗？" n; then
    ask v "允许的客户端名字，逗号分隔" "$(join_csv "${ALLOW_CLIENTS[@]}")"
    csv_to_array ALLOW_CLIENTS "$v"
  fi
  if yesno "要限制隧道能转到哪里吗？" n; then
    ask v "允许的目标，逗号分隔，如 10.0.0.0/8,127.0.0.1:80" "$(join_csv "${ALLOW_TARGETS[@]}")"
    csv_to_array ALLOW_TARGETS "$v"
  fi
}

print_expose_help() {
  cat >/dev/tty <<'EOF'

隧道可选，一条写一种，直接回车结束。
都是「服务器上开门，流量从客户端这边出去」。
只写端口时，口开在服务器本机（127.0.0.1）。要对公网开，写成 0.0.0.0:端口。

  TCP（默认；服务器这个口 → 客户端那个地址）:
    9000=127.0.0.1:80
    tcp://9000=127.0.0.1:80
    0.0.0.0:9000=127.0.0.1:80
    tcp://0.0.0.0:9000=10.0.0.8:22

  UDP:
    udp://5353=127.0.0.1:53
    udp://0.0.0.0:5353=1.1.1.1:53

  SOCKS5（不写目标，连的人自己指定；密码是访问 token）:
    socks://1080
    socks5://1080
    socks://0.0.0.0:1080

  HTTP 代理（不写目标；Basic 认证，密码是访问 token）:
    http://3128
    http://127.0.0.1:3128
    http://0.0.0.0:3128

EOF
}

prompt_client() {
  echo >/dev/tty
  echo "—— 客户端配置（直接回车用括号里的值）——" >/dev/tty
  local v host
  host=$(hostname -s 2>/dev/null || hostname || echo office)
  ask_required SERVER_URL "主服务器地址（ws://IP:8080）" "$SERVER_URL"
  SERVER_URL=$(norm_server "$SERVER_URL")
  ask_required AGENT_TOKEN "隧道口令（跟服务端 agent_token 一样）" "$AGENT_TOKEN"
  ask CLIENT_ID "这台机器的名字" "${CLIENT_ID:-$host}"
  ask RUN_USER "跑客户端的用户（外人连上就是这个用户的命令行）" "${RUN_USER:-${SUDO_USER:-$host}}"

  if [[ ${#EXPOSES[@]} -gt 0 ]]; then
    echo "现在的隧道: $(join_csv "${EXPOSES[@]}")" >/dev/tty
    if yesno "要重新填写隧道吗？" n; then
      EXPOSES=()
    fi
  fi
  if [[ ${#EXPOSES[@]} -eq 0 ]]; then
    print_expose_help
    while true; do
      ask v "再加一条隧道（回车结束）" ""
      [[ -z "$v" ]] && break
      EXPOSES+=("$v")
    done
  fi

  if yesno "要限制本机隧道能连出到哪里吗？" n; then
    ask v "允许的目标，逗号分隔" "$(join_csv "${ALLOW_TARGETS[@]}")"
    csv_to_array ALLOW_TARGETS "$v"
  fi
}

prompt_if_needed() {
  case "$1" in
    server)
      if want_ask; then
        [[ -f "$SERVER_CONF" && "$FORCE" -eq 0 && "$ASK" -eq 0 ]] && load_server_file
        prompt_server
      fi
      [[ -n "$AGENT_TOKEN" ]] || AGENT_TOKEN=$(rand_token)
      [[ -n "$ACCESS_TOKEN" ]] || ACCESS_TOKEN=$(rand_token)
      HTTP=$(norm_listen "$HTTP" ":8080")
      SSH_ADDR=$(norm_listen "$SSH_ADDR" ":2222")
      ;;
    client)
      if want_ask; then
        [[ -f "$CLIENT_CONF" && "$FORCE" -eq 0 && "$ASK" -eq 0 ]] && load_client_file
        prompt_client
      fi
      SERVER_URL=$(norm_server "$SERVER_URL")
      [[ -n "$SERVER_URL" ]] || die "客户端必须指定 --server，或用问答模式填写"
      [[ -n "$AGENT_TOKEN" ]] || die "客户端必须指定 --agent-token，或用问答模式填写"
      ;;
  esac
}

build_linux() {
  local dest=$1 arch=$2
  local root
  root=$(here_dir)
  [[ -f "$root/go.mod" ]] || die "请在仓库目录执行，或用 --bin 指定现成程序"
  need_cmd go
  echo "正在编译 linux/$arch …"
  (
    cd "$root"
    GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$dest" ./cmd/wsproxy
  )
}

do_remote() {
  local target=$1
  shift
  need_cmd ssh
  need_cmd scp
  need_cmd go

  local machine arch tmp
  machine=$(ssh -o BatchMode=yes "$target" uname -m) || die "SSH 连不上 $target（先确认能免密或已有密钥）"
  arch=$(map_arch "$machine")
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  build_linux "$tmp/wsproxy" "$arch"
  cp "$(here_dir)/install.sh" "$tmp/install.sh"
  chmod +x "$tmp/wsproxy" "$tmp/install.sh"

  echo "正在拷到 $target …"
  ssh "$target" 'mkdir -p /tmp/wsproxy-install'
  scp -q "$tmp/wsproxy" "$tmp/install.sh" "$target:/tmp/wsproxy-install/"

  local sudo=""
  if [[ "$target" != root@* && "$target" != root ]]; then
    sudo="sudo "
  fi
  local quoted=""
  printf -v quoted '%q ' --bin /tmp/wsproxy-install/wsproxy "$@"
  # shellcheck disable=SC2029
  ssh -t "$target" "${sudo}bash /tmp/wsproxy-install/install.sh $quoted"
}

install_binary() {
  local dest="$PREFIX/bin/wsproxy"
  local root
  root=$(here_dir)
  mkdir -p "$PREFIX/bin"
  if [[ -n "$BIN" ]]; then
    [[ -x "$BIN" || -f "$BIN" ]] || die "找不到程序: $BIN"
    install -m 0755 "$BIN" "$dest"
  elif [[ "$FROM_SOURCE" -eq 1 ]]; then
    [[ -n "$root" && -f "$root/go.mod" ]] || die "当前目录不是仓库，不能 --from-source"
    need_cmd go
    build_linux "$dest" "$(map_arch "$(uname -m)")"
    chmod 0755 "$dest"
  elif download_release "$dest"; then
    :
  elif [[ -n "$root" && -f "$root/go.mod" ]]; then
    echo "下载发行包失败，改成本地编译"
    need_cmd go
    build_linux "$dest" "$(map_arch "$(uname -m)")"
    chmod 0755 "$dest"
  else
    die "下载最新版失败。检查能不能访问 GitHub，或加 --bin"
  fi
  echo "程序: $dest"
  if [[ -x "$dest" ]]; then
    echo "版本: $("$dest" version 2>/dev/null || echo 未知)"
  fi
}

do_upgrade() {
  install_binary
  if [[ -f "$SERVER_CONF" ]]; then
    local su
    su=$(systemctl show -p User --value wsproxy-server 2>/dev/null || echo wsproxy)
    [[ -n "$su" ]] || su=wsproxy
    fix_conf_perm "$SERVER_CONF" "$su"
  fi
  if [[ -f "$CLIENT_CONF" ]]; then
    local cu
    cu=$(systemctl show -p User --value wsproxy-client 2>/dev/null || echo "${SUDO_USER:-root}")
    [[ -n "$cu" ]] || cu=${SUDO_USER:-root}
    fix_conf_perm "$CLIENT_CONF" "$cu"
  fi
  restart_unit wsproxy-server
  restart_unit wsproxy-client
  echo
  echo "已更新到上面显示的版本。配置没动。"
}

ensure_user() {
  local user=$1 home=$2
  if id "$user" >/dev/null 2>&1; then
    return
  fi
  if command -v useradd >/dev/null 2>&1; then
    useradd --system --home-dir "$home" --create-home --shell /usr/sbin/nologin "$user" 2>/dev/null \
      || useradd --system --home-dir "$home" --create-home --shell /bin/false "$user"
  else
    die "无法创建用户 $user"
  fi
}

have_systemd() {
  [[ "$NO_SYSTEMD" -eq 0 ]] || return 1
  command -v systemctl >/dev/null 2>&1 || return 1
  [[ -d /run/systemd/system ]] || return 1
  case "$(systemctl is-system-running 2>/dev/null || true)" in
    running | degraded | maintenance) return 0 ;;
    *) return 1 ;;
  esac
}

bg_pidfile() {
  echo "$DATA_DIR/${1}.pid"
}

bg_logfile() {
  echo "$DATA_DIR/${1}.log"
}

read_pid() {
  tr -d ' \n' <"$1" 2>/dev/null || true
}

bg_running() {
  local pidf pid
  pidf=$(bg_pidfile "$1")
  [[ -f "$pidf" ]] || return 1
  pid=$(read_pid "$pidf")
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

start_bg() {
  local role=$1 user=$2 conf=$3
  local pidf log pid
  pidf=$(bg_pidfile "$role")
  log=$(bg_logfile "$role")
  mkdir -p "$DATA_DIR"
  if bg_running "$role"; then
    echo "已在后台运行（pid $(read_pid "$pidf")），日志 $log"
    return
  fi
  touch "$log"
  chown "$user:$user" "$DATA_DIR" "$log" 2>/dev/null || true
  nohup "$PREFIX/bin/wsproxy" "$role" --config "$conf" >>"$log" 2>&1 &
  pid=$!
  echo "$pid" >"$pidf"
  sleep 0.4
  if kill -0 "$pid" 2>/dev/null; then
    echo "没有 systemd，已用后台进程启动 $role（pid $pid）"
    echo "日志: tail -f $log"
    echo "停止: $0 stop $role"
  else
    echo "后台启动失败，看日志: $log" >&2
    return 1
  fi
}

stop_bg() {
  local role=$1
  local pidf pid
  pidf=$(bg_pidfile "$role")
  if ! bg_running "$role"; then
    echo "$role 没在后台跑"
    rm -f "$pidf"
    return
  fi
  pid=$(read_pid "$pidf")
  kill "$pid" 2>/dev/null || true
  sleep 0.3
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$pidf"
  echo "已停止 $role"
}

status_bg() {
  local role=$1
  if bg_running "$role"; then
    echo "$role 在跑，pid $(read_pid "$(bg_pidfile "$role")")"
    echo "日志 $(bg_logfile "$role")"
  else
    echo "$role 没在跑"
    return 1
  fi
}

do_service() {
  local action=$1
  local role=${2:-}
  if [[ -z "$role" ]]; then
    if [[ -f "$CLIENT_CONF" ]]; then
      role=client
    elif [[ -f "$SERVER_CONF" ]]; then
      role=server
    else
      die "请写 start/stop/status client 或 server"
    fi
  fi
  case "$role" in
    client | server) ;;
    *) die "只能是 client 或 server" ;;
  esac
  local unit="wsproxy-${role}"
  local conf="$CONF_DIR/${role}.yaml"
  if have_systemd && systemctl cat "$unit" >/dev/null 2>&1; then
    systemctl "$action" "$unit"
    return
  fi
  case "$action" in
    start)
      local user=root
      if [[ "$role" == client ]]; then
        user=${RUN_USER:-${SUDO_USER:-root}}
      else
        user=${RUN_USER:-wsproxy}
      fi
      start_bg "$role" "$user" "$conf"
      ;;
    stop) stop_bg "$role" ;;
    status) status_bg "$role" ;;
    restart)
      stop_bg "$role" || true
      do_service start "$role"
      ;;
    *) die "未知动作 $action" ;;
  esac
}

restart_unit() {
  local name=$1
  local role=${name#wsproxy-}
  if have_systemd && systemctl cat "$name" >/dev/null 2>&1; then
    systemctl daemon-reload
    systemctl restart "$name"
    systemctl enable "$name" >/dev/null
    echo "已重启: systemctl status $name"
    return
  fi
  if [[ -f "$CONF_DIR/${role}.yaml" ]]; then
    do_service restart "$role"
  fi
}

write_unit() {
  local name=$1 user=$2 workdir=$3 conf=$4 role=$5
  if ! have_systemd; then
    echo "这台机器没有可用的 systemd，改用后台进程（断线会自己重连）。"
    start_bg "$role" "$user" "$conf"
    return
  fi
  cat >"/etc/systemd/system/${name}.service" <<EOF
[Unit]
Description=wsproxy ${role}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=${user}
Group=${user}
WorkingDirectory=${workdir}
ExecStart=${PREFIX}/bin/wsproxy ${role} --config ${conf}
Restart=always
RestartSec=2
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now "$name"
  echo "已启动: systemctl status $name"
}

write_server_yaml() {
  local user=$1
  mkdir -p "$CONF_DIR" "$DATA_DIR"
  {
    echo "http: $(yaml_str "$HTTP")"
    echo "ssh: $(yaml_str "$SSH_ADDR")"
    echo "agent_token: $(yaml_str "$AGENT_TOKEN")"
    echo "access_token: $(yaml_str "$ACCESS_TOKEN")"
    echo "host_key: ${DATA_DIR}/ssh_host_key"
    write_yaml_list allow_ips "${ALLOW_IPS[@]+"${ALLOW_IPS[@]}"}"
    write_yaml_list allow_clients "${ALLOW_CLIENTS[@]+"${ALLOW_CLIENTS[@]}"}"
    write_yaml_list allow_targets "${ALLOW_TARGETS[@]+"${ALLOW_TARGETS[@]}"}"
  } >"$SERVER_CONF"
  fix_conf_perm "$SERVER_CONF" "$user"
  chown -R "$user:$user" "$DATA_DIR"
}

fix_conf_perm() {
  local conf=$1 user=$2
  [[ -f "$conf" ]] || return
  mkdir -p "$(dirname "$conf")"
  if [[ "$user" == root ]]; then
    chown root:root "$conf"
    chmod 600 "$conf"
  else
    chown "root:$user" "$conf" 2>/dev/null || chown "$user:$user" "$conf"
    chmod 640 "$conf"
  fi
}

write_client_yaml() {
  local user=$1
  mkdir -p "$CONF_DIR" "$DATA_DIR"
  {
    echo "server: $(yaml_str "$SERVER_URL")"
    echo "agent_token: $(yaml_str "$AGENT_TOKEN")"
    if [[ -n "$CLIENT_ID" ]]; then
      echo "id: $(yaml_str "$CLIENT_ID")"
    fi
    write_yaml_list expose "${EXPOSES[@]+"${EXPOSES[@]}"}"
    write_yaml_list allow_targets "${ALLOW_TARGETS[@]+"${ALLOW_TARGETS[@]}"}"
  } >"$CLIENT_CONF"
  fix_conf_perm "$CLIENT_CONF" "$user"
  if [[ "$user" != root ]]; then
    chown -R "$user:$user" "$DATA_DIR" 2>/dev/null || true
  fi
}

print_server_done() {
  echo
  echo "服务端配置已写入 $SERVER_CONF"
  echo "请把防火墙里的 HTTP 口和 SSH 口放行（现在是 ${HTTP}、${SSH_ADDR}）。"
  echo "网页: http://这台机器的IP${HTTP}"
  echo "SSH:  ssh 客户端名@这台机器的IP -p ${SSH_ADDR##*:}"
  echo
  echo "隧道口令 agent_token:  $AGENT_TOKEN"
  echo "访问 token access_token: $ACCESS_TOKEN"
  echo
  echo "另一台机器装客户端："
  echo "  curl -fsSL https://github.com/${GITHUB_REPO}/releases/latest/download/install.sh | sudo bash -s -- client --server ws://这台IP${HTTP} --agent-token $AGENT_TOKEN --id office"
  echo "测连通: wsproxy test server --config /etc/wsproxy/server.yaml"
}

apply_server() {
  local user=${RUN_USER:-wsproxy}
  local conf_exists=0
  [[ -f "$SERVER_CONF" ]] && conf_exists=1

  mkdir -p "$CONF_DIR" "$DATA_DIR"
  ensure_user "$user" "$DATA_DIR"

  if [[ "$conf_exists" -eq 1 && "$FORCE" -eq 0 && "$YES" -eq 1 && "$ASK" -eq 0 ]]; then
    echo "已有配置 $SERVER_CONF，不改口令。要重写请加 --force 或运行: sudo ./install.sh config"
  else
    write_server_yaml "$user"
  fi
  write_unit wsproxy-server "$user" "$DATA_DIR" "$SERVER_CONF" server
  if [[ -f "$SERVER_CONF" ]]; then
    AGENT_TOKEN=$(yaml_get "$SERVER_CONF" agent_token)
    ACCESS_TOKEN=$(yaml_get "$SERVER_CONF" access_token)
    HTTP=$(yaml_get "$SERVER_CONF" http)
    SSH_ADDR=$(yaml_get "$SERVER_CONF" ssh)
  fi
  print_server_done
}

apply_client() {
  local user=${RUN_USER:-}
  if [[ -z "$user" ]]; then
    user=${SUDO_USER:-}
  fi
  if [[ -z "$user" || "$user" == root ]]; then
    user=root
    echo "注意: 客户端正以 root 跑，外人连上就是 root 的命令行。建议改成普通账号。"
  fi
  if [[ "$user" != root ]]; then
    id "$user" >/dev/null 2>&1 || die "没有用户 $user"
  fi

  mkdir -p "$CONF_DIR" "$DATA_DIR"
  if [[ -f "$CLIENT_CONF" && "$FORCE" -eq 0 && "$YES" -eq 1 && "$ASK" -eq 0 ]]; then
    echo "已有配置 $CLIENT_CONF，不覆盖。要重写请加 --force 或运行: sudo ./install.sh config"
  else
    write_client_yaml "$user"
  fi
  write_unit wsproxy-client "$user" "$DATA_DIR" "$CLIENT_CONF" client
  echo
  echo "客户端配置已写入 $CLIENT_CONF ，会自动连 $SERVER_URL"
  if have_systemd; then
    echo "测连通: wsproxy test client --config /etc/wsproxy/client.yaml"
  else
    echo "没有 systemd。看日志: tail -f $DATA_DIR/client.log"
    echo "停止: $0 stop client    启动: $0 start client"
    echo "或前台跑: wsproxy client --config $CLIENT_CONF"
    echo "测连通: wsproxy test client --config $CLIENT_CONF"
  fi
}

do_config() {
  local which=""
  local has_s=0 has_c=0
  [[ -f "$SERVER_CONF" ]] && has_s=1
  [[ -f "$CLIENT_CONF" ]] && has_c=1
  if [[ "$has_s" -eq 0 && "$has_c" -eq 0 ]]; then
    die "还没有配置。先 sudo ./install.sh server 或 client"
  fi

  if ! have_tty; then
    die "改配置需要交互终端。在机器上运行: sudo ./install.sh config"
  fi

  if [[ "$has_s" -eq 1 && "$has_c" -eq 1 ]]; then
    echo >/dev/tty
    echo "  1) 改服务端 $SERVER_CONF" >/dev/tty
    echo "  2) 改客户端 $CLIENT_CONF" >/dev/tty
    ask which "改哪一份" "1"
  elif [[ "$has_s" -eq 1 ]]; then
    which=1
  else
    which=2
  fi

  case "$which" in
    1 | server)
      load_server_file
      if [[ -d /run/systemd/system ]] && systemctl show -p User --value wsproxy-server >/dev/null 2>&1; then
        RUN_USER=$(systemctl show -p User --value wsproxy-server 2>/dev/null || echo wsproxy)
      fi
      [[ -n "$RUN_USER" ]] || RUN_USER=wsproxy
      prompt_server
      [[ -n "$AGENT_TOKEN" ]] || AGENT_TOKEN=$(rand_token)
      [[ -n "$ACCESS_TOKEN" ]] || ACCESS_TOKEN=$(rand_token)
      HTTP=$(norm_listen "$HTTP" ":8080")
      SSH_ADDR=$(norm_listen "$SSH_ADDR" ":2222")
      write_server_yaml "${RUN_USER:-wsproxy}"
      restart_unit wsproxy-server
      print_server_done
      ;;
    2 | client)
      load_client_file
      if [[ -d /run/systemd/system ]]; then
        RUN_USER=$(systemctl show -p User --value wsproxy-client 2>/dev/null || true)
      fi
      prompt_client
      SERVER_URL=$(norm_server "$SERVER_URL")
      write_client_yaml "${RUN_USER:-${SUDO_USER:-root}}"
      restart_unit wsproxy-client
      echo
      echo "客户端配置已更新，已按新配置重连 $SERVER_URL"
      ;;
    *)
      die "无法识别: $which"
      ;;
  esac
}

do_uninstall() {
  if [[ -d /run/systemd/system ]]; then
    systemctl disable --now wsproxy-server 2>/dev/null || true
    systemctl disable --now wsproxy-client 2>/dev/null || true
    rm -f /etc/systemd/system/wsproxy-server.service /etc/systemd/system/wsproxy-client.service
    systemctl daemon-reload
  fi
  stop_bg server 2>/dev/null || true
  stop_bg client 2>/dev/null || true
  rm -f "$PREFIX/bin/wsproxy"
  if have_tty && yesno "连配置一起删掉（/etc/wsproxy、/var/lib/wsproxy）？" n; then
    rm -rf "$CONF_DIR" "$DATA_DIR"
    echo "已经卸干净。"
  else
    echo "程序和开机服务已去掉。配置还在 $CONF_DIR"
  fi
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi
  if [[ "${1:-}" == remote ]]; then
    [[ $# -ge 2 ]] || { usage; exit 2; }
    local target=$2
    shift 2
    do_remote "$target" "$@"
    return
  fi
  if [[ "${1:-}" == start || "${1:-}" == stop || "${1:-}" == status ]]; then
    is_root || die "请用 sudo 运行"
    do_service "$1" "${2:-}"
    return
  fi

  parse_args "$@"
  if [[ -z "$ROLE" ]]; then
    if have_tty; then
      prompt_menu
    else
      usage
      exit 2
    fi
  fi

  is_root || die "请用 sudo 运行（远端安装: ./install.sh remote 用户@主机）"

  case "$ROLE" in
    uninstall)
      do_uninstall
      ;;
    config)
      do_config
      ;;
    upgrade)
      do_upgrade
      ;;
    server)
      prompt_if_needed server
      install_binary
      apply_server
      ;;
    client)
      prompt_if_needed client
      install_binary
      apply_client
      ;;
  esac
}

main "$@"
