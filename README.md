# wsproxy

客户端主动用 WebSocket 连上主服务器。服务器把外人的 SSH 或网页终端转到客户端本机命令行。客户端**不用开 sshd**。

思路来自 [wstunnel](https://github.com/erebe/wstunnel) / [chisel](https://github.com/jpillora/chisel) 的「连出再反向转」，SSH 入口用 [gliderlabs/ssh](https://github.com/gliderlabs/ssh)，本机命令行用 [creack/pty](https://github.com/creack/pty)。没有去整仓 fork ShellHub：那套要数据库和一堆容器，和我们要的轻量中转不是一回事。

## 怎么走

```
外人 ssh / 浏览器
        │
        ▼
   主服务器（验 token）
        │  WebSocket
        ▼
   客户端（本机 bash）
```

两个口令分开：

- `--agent-token`：只有客户端连服务器时用
- `--access-token`：发给外人，用在 SSH 密码或网页登录里

## 安装

本机编译：

```bash
go build -o wsproxy ./cmd/wsproxy
```

Linux 远程安装（在仓库目录执行，会编译并拷过去；远端一项项问配置）：

```bash
./install.sh remote 用户@那台机器
```

已经装过的机器改配置（改完自动重启）：

```bash
sudo ./install.sh config
```

不写参数会出菜单。也可以直接 `sudo ./install.sh server` 或 `client`，按提示填口令、端口、白名单。全部用参数、不问话时加 `--yes`。

## 用 YAML 配置

推荐把口令和隧道写进文件，命令行只用来临时改一项。

```bash
./wsproxy server --config examples/server.yaml
./wsproxy client --config examples/client.yaml
```

一份文件里也可以同时写 `server:` 和 `client:`，见 `examples/all.yaml`。命令行写过的项会覆盖文件，例如：

```bash
./wsproxy client --config examples/client.yaml --id home --expose http://127.0.0.1:3128
```

客户端 `expose` 支持一行字符串，或拆开写：

```yaml
expose:
  - 9000=127.0.0.1:80
  - kind: socks
    listen: 127.0.0.1:1080
```

## 启动

**主服务器：**

```bash
./wsproxy server \
  --http :8080 \
  --ssh :2222 \
  --agent-token 改成隧道口令 \
  --access-token 改成访问token
```

**客户端（每台机器一个名字，不开 SSH）：**

```bash
./wsproxy client \
  --server ws://你的服务器IP:8080 \
  --agent-token 改成隧道口令 \
  --id office
```

再开一台就换个 `--id`，例如 `--id home`。不写 `--id` 时用这台机器的主机名。同名再连会顶掉旧连接。

## 外人怎么连

SSH（用户名是客户端名字，密码是访问 token）：

```bash
ssh office@你的服务器IP -p 2222
ssh home@你的服务器IP -p 2222
```

只有一台在线时，也可以 `ssh 访问token@服务器 -p 2222`。多台同时在线时，必须写客户端名字。

网页：打开 `http://你的服务器IP:8080/`，在页面里输入访问 token。登录后 token 记在 Cookie 里，不会出现在地址栏。多台在线会列出机器，点进去；只有一台会直接进命令行。

## 隧道

都是「服务器上开门，流量从客户端那边出去」。可以写多条 `--expose`。

**只写端口时，默认听服务器本机**（`127.0.0.1`）。要对公网开，必须显式写 `0.0.0.0:端口`。

**固定 TCP：** 服务器本机 `9000` → 客户端本机 `80`

```bash
./wsproxy client --server ws://服务器IP:8080 --agent-token 隧道口令 --id office \
  --expose 9000=127.0.0.1:80
```

**固定 UDP：**

```bash
--expose udp://5353=127.0.0.1:53
```

**SOCKS5：** 访问哪里由使用方决定，从客户端出去。必须用访问 token 当密码。

```bash
--expose socks://1080
```

```bash
curl -x socks5h://u:访问token@127.0.0.1:1080 https://example.com
```

要给外人直连服务器这个口，写成 `socks://0.0.0.0:1080`，并仍然需要 token。建议再配 `allow_targets`，避免被当成开放代理。

**HTTP 代理：** 同样要带访问 token（Basic 认证），支持普通 HTTP 和 `CONNECT`（HTTPS）

```bash
--expose http://3128
curl -x http://u:访问token@127.0.0.1:3128 https://example.com
```

## 白名单

不写就全放行。写了只放名单里的。命令行 `--allow-ip` / `--allow-client` / `--allow-target` 可重复，会加到配置文件后面。

```yaml
server:
  allow_ips: [127.0.0.1, 10.0.0.0/8]   # 谁能连网页、SSH、隧道入口；/health 和客户端连入不拦
  allow_clients: [office, home]         # 哪些客户端名字能连上来
  allow_targets: [10.0.0.0/8, 127.0.0.1:80]  # 隧道允许转到哪里
client:
  allow_targets: [127.0.0.1:80, 10.0.0.0/8]  # 本机允许连出到哪里
```

条目可以是 IP、网段、`IP:端口`、主机名、`*.domain` 或 `*`。

## 注意

- 明文 `ws://` 只适合本机或内网试跑。对外请在前面加 Nginx/Caddy 做 HTTPS，客户端改成 `wss://`。
- 网页 token 走登录页和 Cookie，不要再把 token 写进网址。SSH 请用客户端名字当用户名，密码填访问 token。
- SOCKS/HTTP 必须带访问 token；只写端口时只听本机。开放代理请再配 `allow_targets`。
- 网页终端从 CDN 拉 xterm；服务器出不了网时，SSH 仍然可用。
- 给出访问 token 等于给出客户端那台机器的命令行，客户端建议用权限较小的账号跑。
