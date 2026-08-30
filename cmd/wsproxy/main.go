package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"wsproxy/internal/allow"
	"wsproxy/internal/check"
	"wsproxy/internal/client"
	"wsproxy/internal/config"
	"wsproxy/internal/proto"
	"wsproxy/internal/server"
	"wsproxy/internal/version"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "server":
		os.Exit(runServer(os.Args[2:]))
	case "client":
		os.Exit(runClient(os.Args[2:]))
	case "test":
		os.Exit(runTest(os.Args[2:]))
	case "version", "-v", "--version":
		fmt.Println(version.String())
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `wsproxy — 客户端连出，服务器中转命令行

用法:
  wsproxy server [--config 文件.yaml] [选项]
  wsproxy client [--config 文件.yaml] [选项]
  wsproxy test [server|client] [--config 文件.yaml]
  wsproxy version

配置文件用 YAML。命令行写过的项会覆盖文件。

服务端:
  --config 文件.yaml
  --http :8080
  --ssh :2222
  --agent-token TOKEN
  --access-token TOKEN
  --host-key ssh_host_key
  --allow-ip 地址            可重复。谁能连网页、SSH、隧道入口（/health 和客户端连入不拦）
  --allow-client 名字        可重复。允许连上来的客户端 id
  --allow-target 地址        可重复。隧道允许转到哪里

客户端:
  --config 文件.yaml
  --server ws://主机:8080
  --agent-token TOKEN
  --id 名字
  --shell /bin/bash
  --expose 规则              可重复，会加在配置文件的隧道后面
  --allow-target 地址        可重复。本机允许连出到哪里

白名单不写就全放行。命令行写的项会加到配置文件后面。
写法：IP、网段、IP:端口、主机名、*.domain、*。

隧道 --expose（只写端口时默认听本机；要对公网开请写 0.0.0.0:端口）:
  9000=127.0.0.1:80
  udp://5353=127.0.0.1:53
  socks://1080
  http://3128
  socks://0.0.0.0:1080
SOCKS/HTTP 要用访问 token 认证（密码填 access_token）。
`)
}

func visited(fs *flag.FlagSet) map[string]string {
	out := map[string]string{}
	fs.Visit(func(f *flag.Flag) {
		out[f.Name] = f.Value.String()
	})
	return out
}

func runServer(args []string) int {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	configPath := fs.String("config", "", "")
	fs.String("http", "", "")
	fs.String("ssh", "", "")
	fs.String("agent-token", "", "")
	fs.String("access-token", "", "")
	fs.String("host-key", "", "")
	var allowIPs, allowClients, allowTargets stringList
	fs.Var(&allowIPs, "allow-ip", "")
	fs.Var(&allowClients, "allow-client", "")
	fs.Var(&allowTargets, "allow-target", "")
	_ = fs.Parse(args)

	var file config.Server
	if *configPath != "" {
		var err error
		file, err = config.LoadServer(*configPath)
		if err != nil {
			slog.Error(err.Error())
			return 2
		}
	}
	cfg := config.MergeServer(file, visited(fs))
	cfg.AllowIPs = append(cfg.AllowIPs, allowIPs...)
	cfg.AllowClients = append(cfg.AllowClients, allowClients...)
	cfg.AllowTargets = append(cfg.AllowTargets, allowTargets...)
	if cfg.AgentToken == "" || cfg.AccessToken == "" {
		slog.Error("必须设置 agent_token 和 access_token（配置文件或命令行）")
		return 2
	}
	sets, err := allow.ParseSets(cfg.AllowIPs, cfg.AllowClients, cfg.AllowTargets)
	if err != nil {
		slog.Error(err.Error())
		return 2
	}
	s := server.New(cfg.HTTP, cfg.SSH, cfg.AgentToken, cfg.AccessToken, cfg.HostKey)
	s.SetAllow(sets)
	if err := s.Run(); err != nil {
		slog.Error("server", "err", err)
		return 1
	}
	return 0
}

func runClient(args []string) int {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	configPath := fs.String("config", "", "")
	fs.String("server", "", "")
	fs.String("agent-token", "", "")
	fs.String("id", "", "")
	fs.String("shell", "", "")
	var exposes exposeFlags
	var allowTargets stringList
	fs.Var(&exposes, "expose", "")
	fs.Var(&allowTargets, "allow-target", "")
	_ = fs.Parse(args)

	var file config.Client
	if *configPath != "" {
		var err error
		file, err = config.LoadClient(*configPath)
		if err != nil {
			slog.Error(err.Error())
			return 2
		}
	}
	cfg := config.MergeClient(file, visited(fs), exposes.items, allowTargets)
	if cfg.Server == "" || cfg.AgentToken == "" {
		slog.Error("必须设置 server 和 agent_token（配置文件或命令行）")
		return 2
	}
	targets, err := allow.Parse(cfg.AllowTargets)
	if err != nil {
		slog.Error(err.Error())
		return 2
	}
	if err := client.Run(client.Config{
		ID:           cfg.ID,
		Server:       cfg.Server,
		AgentToken:   cfg.AgentToken,
		Shell:        cfg.Shell,
		Exposes:      cfg.Exposes(),
		AllowTargets: targets,
	}); err != nil {
		slog.Error("client", "err", err)
		return 1
	}
	return 0
}

func runTest(args []string) int {
	role := ""
	if len(args) > 0 && (args[0] == "server" || args[0] == "client") {
		role = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	configPath := fs.String("config", "", "")
	fs.String("http", "", "")
	fs.String("ssh", "", "")
	fs.String("server", "", "")
	fs.String("agent-token", "", "")
	fs.String("access-token", "", "")
	fs.String("id", "", "")
	_ = fs.Parse(args)
	set := visited(fs)

	defServer, defClient := check.DefaultConfigs()
	path := *configPath
	okAll := true

	runS := role == "server" || (role == "" && (path != "" || fileOK(defServer)))
	runC := role == "client" || (role == "" && (path != "" || fileOK(defClient)))
	if role == "" && path == "" {
		runS = fileOK(defServer)
		runC = fileOK(defClient)
	}

	if !runS && !runC {
		if path != "" {
			runS, runC = true, true
		} else {
			slog.Error("找不到配置。加上 --config，或把文件放在 /etc/wsproxy/server.yaml、client.yaml")
			return 2
		}
	}

	if runS {
		file, err := loadServerFile(path, defServer)
		if err != nil && role == "server" {
			slog.Error(err.Error())
			return 2
		}
		if err != nil {
			slog.Error(err.Error())
			if role == "server" {
				return 2
			}
		} else if file.AccessToken != "" || file.HTTP != "" || role == "server" {
			cfg := config.MergeServer(file, set)
			rep := check.Server(cfg)
			if !rep.OK {
				okAll = false
			}
		} else if role == "server" {
			slog.Error("这不像服务端配置")
			return 2
		}
	}
	if runC {
		file, err := loadClientFile(path, defClient)
		if err != nil {
			slog.Error(err.Error())
			if role == "client" {
				return 2
			}
		} else {
			cfg := config.MergeClient(file, set, nil, nil)
			if cfg.Server == "" {
				fmt.Println("配置里没有主服务器地址。")
				cfg.Server = check.Prompt("请输入主服务器地址（例如 ws://1.2.3.4:8080）: ")
			}
			if cfg.AgentToken == "" {
				cfg.AgentToken = check.Prompt("请输入隧道口令 agent_token: ")
			}
			if cfg.Server == "" {
				slog.Error("没有服务器地址，测不了是否连通")
				return 2
			}
			if runS {
				fmt.Println()
			}
			rep := check.Client(cfg)
			if !rep.OK {
				okAll = false
			}
		}
	}
	if !okAll {
		return 1
	}
	return 0
}

func fileOK(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func loadServerFile(path, fallback string) (config.Server, error) {
	if path != "" {
		return config.LoadServer(path)
	}
	return config.LoadServer(fallback)
}

func loadClientFile(path, fallback string) (config.Client, error) {
	if path != "" {
		return config.LoadClient(path)
	}
	return config.LoadClient(fallback)
}

type stringList []string

func (s *stringList) String() string { return "" }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type exposeFlags struct {
	items []proto.Expose
}

func (e *exposeFlags) String() string { return "" }

func (e *exposeFlags) Set(v string) error {
	item, err := client.ParseExpose(v)
	if err != nil {
		return err
	}
	e.items = append(e.items, item)
	return nil
}
