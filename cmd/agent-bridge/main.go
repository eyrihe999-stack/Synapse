// agent-bridge 用户机器上跑的 daemon —— 把 Synapse 推过来的 mention 事件桥接到本地
// claude -p 子进程,让 Claude Code 自动响应被 @ 的消息。
//
// 工作模型:
//
//  1. 用 PAT 长连 GET <synapse>/api/v2/users/me/events (Server-Sent Events)
//  2. 收到 event=mention.received 帧 → fork claude -p,prompt 里塞 mention 索引
//  3. Claude 子进程自己用 MCP tool(list_my_mentions / get_task_context / post_message...)
//     拉详情 + 决定怎么回。daemon 不负责拼上下文,假设用户已配好 Synapse MCP server
//  4. 网络断 → 指数退避重连;Ctrl+C → 优雅退出
//
// 子命令:
//
//	agent-bridge              跑 daemon(默认)—— 需要先 setup
//	agent-bridge setup        首次安装,交互式问答 base_url + PAT,写到 config 文件
//	agent-bridge version      打印版本
//	agent-bridge help         查看帮助
//
// 配置:`~/.synapse-agent/config.json`(权限 0600,含 PAT 不可外读)。
// env vars 仍可 override config(开发期 / 多账号切换方便):
//
//	SYNAPSE_AGENT_BASE_URL  覆盖 base_url
//	SYNAPSE_AGENT_PAT       覆盖 pat
//	SYNAPSE_AGENT_CLAUDE    覆盖 claude binary 路径
//	SYNAPSE_AGENT_DRY_RUN   "1" 只打印 fork 命令,不真起 claude(联调用)
//
// 安全提醒:
//
//	daemon 给 claude 传 --dangerously-skip-permissions(没 TTY 没法弹确认)。
//	这意味着 claude 在 daemon 工作目录里能读写文件、跑 Bash。生产部署务必把
//	daemon 切到一个隔离 cwd(类似 ~/.synapse-agent/),并依赖 user 自己配的
//	~/.claude/CLAUDE.md 收紧行为(或后续版本加 --allowedTools 白名单)。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// version 由 build ldflags 注入:-X main.version=v0.1.0;不注入就是 "dev"。
var version = "dev"

const (
	defaultBaseURL = "http://localhost:8080"
	defaultClaude  = "claude"
	configDirName  = ".synapse-agent"
	configFileName = "config.json"
)

// ─── 配置 ─────────────────────────────────────────────────────────────────────

type config struct {
	BaseURL   string `json:"base_url"`
	PAT       string `json:"pat"`
	ClaudeBin string `json:"claude_bin"`

	// DryRun 只从 env 读,不持久化(联调态用,不该写进永久 config)
	DryRun bool `json:"-"`
}

// configPath 返回 ~/.synapse-agent/config.json 绝对路径。
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, configDirName, configFileName), nil
}

// loadConfig 从磁盘加载 config + env vars 覆盖。
// config 不存在 → 给清晰错误提示用户先跑 `agent-bridge setup`。
func loadConfig() (*config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config 不存在 (%s) —— 先跑 `agent-bridge setup` 初始化", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// env vars 覆盖(开发期 / 多账号切换方便)
	if v := strings.TrimRight(os.Getenv("SYNAPSE_AGENT_BASE_URL"), "/"); v != "" {
		c.BaseURL = v
	}
	if v := os.Getenv("SYNAPSE_AGENT_PAT"); v != "" {
		c.PAT = v
	}
	if v := os.Getenv("SYNAPSE_AGENT_CLAUDE"); v != "" {
		c.ClaudeBin = v
	}
	c.DryRun = os.Getenv("SYNAPSE_AGENT_DRY_RUN") == "1"

	if c.BaseURL == "" {
		return nil, errors.New("config base_url 为空,请重新跑 `agent-bridge setup`")
	}
	if c.PAT == "" {
		return nil, errors.New("config pat 为空,请重新跑 `agent-bridge setup`")
	}
	if c.ClaudeBin == "" {
		c.ClaudeBin = defaultClaude
	}
	return &c, nil
}

// saveConfig 写 config 到磁盘。目录权限 0700,文件权限 0600 —— 含 PAT,不能让
// 同机器其他 user 读到。
func saveConfig(c *config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ─── setup 子命令 ────────────────────────────────────────────────────────────

// runSetup 交互式引导用户填 base_url + PAT,写 config 文件。
// 已有 config 时把现值当默认值,直接 enter 复用。
func runSetup() error {
	fmt.Printf("agent-bridge setup (version %s)\n", version)
	fmt.Print("这一步把 base url + PAT 写到 ~/.synapse-agent/config.json,以后启动 daemon 直接读。\n\n")

	// 读已有 config 作为默认值
	var existing config
	if path, err := configPath(); err == nil {
		if raw, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(raw, &existing)
		}
	}
	if existing.BaseURL == "" {
		existing.BaseURL = defaultBaseURL
	}
	if existing.ClaudeBin == "" {
		existing.ClaudeBin = defaultClaude
	}

	reader := bufio.NewReader(os.Stdin)

	// 1. base url
	baseURL := promptDefault(reader, "Synapse base URL", existing.BaseURL)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return errors.New("base url 不能为空")
	}

	// 2. PAT
	fmt.Printf("\nPAT 怎么生成:浏览器打开 %s/user/security 创建一个 token 复制过来。\n", baseURL)
	patHint := ""
	if existing.PAT != "" {
		patHint = "保留现有"
	}
	pat := promptDefault(reader, "PAT (paste then enter)", patHint)
	if pat == "保留现有" {
		pat = existing.PAT
	}
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return errors.New("PAT 不能为空")
	}
	if !strings.HasPrefix(pat, "syn_") {
		fmt.Print("\n警告:PAT 不是 syn_ 前缀,可能不是有效 token。继续按 enter,放弃按 Ctrl+C…")
		_, _ = reader.ReadString('\n')
	}

	// 3. claude binary 检测 —— daemon fork claude 的目标。**必须装好**才有意义,
	// 没装 setup 干脆中止,引导用户去装,避免写下一个跑不动的 config。
	claudeBin := existing.ClaudeBin
	if found, err := exec.LookPath(claudeBin); err == nil {
		fmt.Printf("\n✓ 检测到 claude:%s\n", found)
	} else {
		fmt.Printf("\n✗ PATH 里找不到 `%s`。\n", claudeBin)
		fmt.Println()
		fmt.Println("Claude Code 是 daemon fork 子进程的目标,必须先装好:")
		fmt.Println()
		fmt.Println("  方式 1(推荐,需 Node.js):")
		fmt.Println("    npm install -g @anthropic-ai/claude-code")
		fmt.Println()
		fmt.Println("  方式 2(官方一键脚本):")
		fmt.Println("    curl -fsSL https://claude.ai/install.sh | bash")
		fmt.Println()
		fmt.Println("装完后:")
		fmt.Println("  1. 跑一次 `claude` 完成登录(选 Pro/Max 订阅或填 API key)")
		fmt.Println("  2. 回来重跑 `agent-bridge setup`")
		fmt.Println()
		fmt.Println("已装但 binary 不在 PATH(罕见)—— 输入完整路径继续;")
		fmt.Println("没装就按回车终止 setup,装完再来。")
		newBin := promptDefault(reader, "完整路径(回车 = 终止)", "")
		if newBin == "" {
			return errors.New("claude 未安装,setup 终止;装完后重跑 `agent-bridge setup`")
		}
		if _, statErr := os.Stat(newBin); statErr != nil {
			return fmt.Errorf("路径不存在或无法访问: %s (%w)", newBin, statErr)
		}
		claudeBin = newBin
	}

	c := &config{
		BaseURL:   baseURL,
		PAT:       pat,
		ClaudeBin: claudeBin,
	}
	if err := saveConfig(c); err != nil {
		return err
	}

	path, _ := configPath()
	fmt.Printf("\n✓ 配置写到 %s\n", path)
	fmt.Print("\n现在可以启动 daemon:\n")
	fmt.Print("  agent-bridge\n")
	fmt.Print("\n后台跑(关 Terminal 不停):\n")
	fmt.Print("  nohup agent-bridge > ~/.synapse-agent/daemon.log 2>&1 &\n")
	fmt.Print("\n看后台日志:\n")
	fmt.Print("  tail -f ~/.synapse-agent/daemon.log\n")
	return nil
}

// promptDefault 打印 "label [default]: " 让用户输入,空 enter 用 default。
func promptDefault(r *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// printUsage 打印 help 信息。
func printUsage() {
	fmt.Printf(`agent-bridge - Synapse local agent daemon (version %s)

USAGE:
  agent-bridge              跑 daemon (默认)
  agent-bridge setup        首次安装,交互式问答 base url + PAT
  agent-bridge version      打印版本
  agent-bridge help         显示本帮助

CONFIG:
  ~/.synapse-agent/config.json  (由 setup 创建)

ENV VARS (override config):
  SYNAPSE_AGENT_BASE_URL    覆盖 base url
  SYNAPSE_AGENT_PAT         覆盖 PAT
  SYNAPSE_AGENT_CLAUDE      覆盖 claude binary 路径
  SYNAPSE_AGENT_DRY_RUN     "1" 只打印 fork 命令,不真起 claude(联调用)
`, version)
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	// 子命令分发。无参数走默认 daemon 流程。
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "setup":
			if err := runSetup(); err != nil {
				fmt.Fprintln(os.Stderr, "setup failed:", err)
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			fmt.Printf("agent-bridge %s\n", version)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
			printUsage()
			os.Exit(2)
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGINT/SIGTERM → cancel ctx → 主循环退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		log.Info("signal received, shutting down", "signal", s.String())
		cancel()
	}()

	log.Info("agent-bridge starting",
		"version", version,
		"base_url", cfg.BaseURL,
		"claude_bin", cfg.ClaudeBin,
		"dry_run", cfg.DryRun,
	)

	// 启动期检测 Claude Code 的 MCP 配置 —— 不主动改 ~/.claude.json,只查 + 提示。
	// 检测失败不阻止启动:用户可能就想验 daemon 链路通不通,不在乎 claude 处理质量。
	preflightCheckMCP(ctx, cfg, log)

	// 主循环:断线指数退避重连。退避上限 30s。
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff
	for ctx.Err() == nil {
		err := stream(ctx, cfg, log)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			log.Warn("stream ended, will retry", "err", err, "backoff", backoff)
		} else {
			log.Info("stream ended cleanly, will reconnect", "backoff", backoff)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	log.Info("agent-bridge stopped")
}

// ─── SSE 客户端 ──────────────────────────────────────────────────────────────

// stream 建立一次 SSE 连接并循环消费,直到连接结束(网络错 / server 主动关 / ctx done)。
// 成功收到第一帧 "ready" 后退避计数器在外层会被外层重置(简化:外层目前没重置,等 v1 优化)。
func stream(ctx context.Context, cfg *config, log *slog.Logger) error {
	url := cfg.BaseURL + "/api/v2/users/me/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.PAT)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// http.Client 默认 Timeout=0(不限时,SSE 必须如此);但要保留 ctx 取消能力。
	// 不用 DefaultClient(它无超时,但保险起见显式构造)。
	httpClient := &http.Client{Timeout: 0}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	log.Info("sse connected", "url", url)

	// 解析 SSE:按行读,event/data 累积到空行触发 dispatch。
	// bufio.Scanner 默认 64KB buffer 够用(单帧极少超 KB);设大点防意外。
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1*1024*1024)

	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			// 帧结束。空 event 名(纯 data 的"message"事件)v0 不期待,跳过。
			if event != "" {
				dispatchEvent(ctx, cfg, log, event, data)
			}
			event, data = "", ""
		case strings.HasPrefix(line, ":"):
			// SSE comment / heartbeat,纯 keepalive
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			// SSE 多行 data 用换行拼,v0 服务端只发单行,直接覆盖
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("scan: %w", err)
	}
	return nil
}

// dispatchEvent 处理一帧 SSE。v0 只关心 mention.received,其他事件类型 log 一下放过。
func dispatchEvent(ctx context.Context, cfg *config, log *slog.Logger, event, data string) {
	switch event {
	case "ready":
		log.Info("sse ready", "data", data)
	case "mention.received":
		log.Info("mention received", "data", data)
		// fork 失败不影响后续事件 —— 串行处理保证 mention 顺序;并发要看 token
		// 成本和服务端速率,留 v1
		spawnClaude(ctx, cfg, log, data)
	default:
		log.Debug("unknown event ignored", "event", event, "data", data)
	}
}

// ─── 启动期 MCP 自检 + 自动配置 ──────────────────────────────────────────────

// preflightCheckMCP 启动时跑一次 `claude mcp list`,按情况决定是否自动跑 `claude mcp add`
// 把 synapse MCP 写到 user scope。
//
// 四种分支:
//   - claude 不在 PATH / 跑不动 → WARN,跳过(daemon 仍启动,但 mention 触发会失败)
//   - 配置不存在 → **自动跑 `claude mcp add --scope user ...` 写入**,失败 WARN + 给可 copy 命令
//   - 配置存在但未连通 → WARN(可能 PAT 失效或 server 不通,**不主动覆盖**避免破坏用户已有
//     自定义配置;让用户跑 `claude mcp remove synapse --scope user` 后重启 daemon 自动重建)
//   - 配置存在且 connected → INFO 正常继续
//
// 任一失败都不阻止 daemon 启动。给 30s 超时:`claude mcp list` 会真去连每个 MCP
// server 测连通,配了多个 server / 某个 server 慢就可能要 10-20s,30s 留点余量。
func preflightCheckMCP(parent context.Context, cfg *config, log *slog.Logger) {
	cctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, cfg.ClaudeBin, "mcp", "list")
	raw, err := cmd.CombinedOutput()
	output := string(raw)

	if err != nil {
		// "signal: killed" = ctx timeout 触发 SIGKILL;其他错通常是 claude 跑不动或参数变了。
		log.Warn("preflight: `claude mcp list` 失败,跳过 MCP 自检",
			"err", err.Error(),
			"hint", "如果是超时(signal: killed)说明 claude mcp list 太慢(可能某个已配置的 MCP server 连不上拖慢);daemon 会继续启动,mention 触发时 fork 出来的 claude 仍会用现有配置",
		)
		return
	}

	if !strings.Contains(output, "synapse") {
		log.Info("preflight: 未在 Claude Code 看到 synapse MCP,自动配置中…")
		if err := autoAddMCP(parent, cfg); err != nil {
			log.Warn("preflight: 自动配置 MCP 失败",
				"err", err.Error(),
				"fix", buildAddCommand(cfg),
			)
			return
		}
		log.Info("preflight: synapse MCP 已自动写入 user scope (~/.claude.json)")
		return
	}

	// `claude mcp list` 输出里 connected 状态用 "✓ Connected" / "✗ Failed to connect" 标识;
	// 大小写敏感性按版本不同,统一 lower 后看 "connected" 关键字保险一点
	if !strings.Contains(strings.ToLower(output), "connected") {
		// 典型原因:用户在 Web 上 revoke 了老 PAT,daemon setup 时填了新 PAT,
		// 但 ~/.claude.json 里 synapse server 还绑老 PAT → 401 未连通。
		// 自动 remove + 重建,用 daemon 当前 config 里的 PAT 同步。
		// 只动 name="synapse" 这一条,其他 MCP server 不受影响。
		log.Info("preflight: synapse MCP 已配置但未连通,自动用当前 PAT 重建...")
		if err := removeMCP(parent, cfg); err != nil {
			log.Warn("preflight: 自动 remove synapse 失败,放弃重建",
				"err", err.Error(),
				"hint", "手动跑 `claude mcp remove synapse --scope user` 后重启 daemon",
			)
			return
		}
		if err := autoAddMCP(parent, cfg); err != nil {
			log.Warn("preflight: 自动重建失败 —— remove 已生效,你需要手动 add",
				"err", err.Error(),
				"fix", buildAddCommand(cfg),
			)
			return
		}
		log.Info("preflight: synapse MCP 已重建(用当前 daemon config 的 PAT 替换老配置)")
		return
	}

	log.Info("preflight: synapse MCP 已配置且连通")
}

// removeMCP 跑 `claude mcp remove synapse --scope user`,删 ~/.claude.json 里 user
// scope 下的 synapse server 配置。用于"已配置但未连通"场景的自动恢复(典型:PAT 失效)。
//
// 安全边界:**只动 name="synapse" 这一条**,其他 MCP server 不受影响。如果用户故意
// 把别的 server 命名成 "synapse",会被覆盖 —— 这种命名碰撞极少见,可接受。
func removeMCP(parent context.Context, cfg *config) error {
	cctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, cfg.ClaudeBin,
		"mcp", "remove", "synapse", "--scope", "user",
	)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(raw)))
	}
	return nil
}

// autoAddMCP 跑 `claude mcp add --scope user --transport http synapse <url> --header ...`
// 把 daemon 当前用的 PAT 写入 ~/.claude.json 全局配置。
//
// 用 user scope 而非 local scope:daemon fork claude 的 cwd 跟用户平时用 claude 的 cwd
// 通常不同,只有 user scope 才能保证 fork 出来的 claude 看得见。
//
// 不主动覆盖已存在的同名 MCP —— 调用方先用 mcp list 确认不存在才会调到这里。
func autoAddMCP(parent context.Context, cfg *config) error {
	cctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	args := []string{
		"mcp", "add",
		"--scope", "user",
		"--transport", "http",
		"synapse",
		cfg.BaseURL + "/api/v2/mcp",
		"--header", "Authorization: Bearer " + cfg.PAT,
	}
	cmd := exec.CommandContext(cctx, cfg.ClaudeBin, args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(raw)))
	}
	return nil
}

// buildAddCommand 拼一条用户可直接 copy-paste 的 `claude mcp add` 命令(自动配置失败时的兜底)。
// 注意:**会把 PAT 明文写进日志输出**,daemon 进程 stderr 反正能看到所有 PAT,
// 提示用户的便利 > stderr 增加一行的泄露面。
func buildAddCommand(cfg *config) string {
	return fmt.Sprintf(
		`claude mcp add --scope user --transport http synapse %s/api/v2/mcp --header "Authorization: Bearer %s"`,
		cfg.BaseURL, cfg.PAT,
	)
}

// ─── claude 子进程 ───────────────────────────────────────────────────────────

// spawnClaude 起一次 claude -p,prompt 里塞 mention payload(JSON 字符串)。
// 阻塞到子进程退出。任何错只 log,不传播 —— daemon 主循环继续消费下一条事件。
func spawnClaude(ctx context.Context, cfg *config, log *slog.Logger, mentionData string) {
	prompt := buildPrompt(mentionData)
	args := []string{"-p", prompt, "--dangerously-skip-permissions"}

	if cfg.DryRun {
		fmt.Printf("[dry-run] %s %v\n  prompt:\n%s\n", cfg.ClaudeBin, args, prompt)
		return
	}

	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	started := time.Now()
	log.Info("spawning claude", "bin", cfg.ClaudeBin)
	err := cmd.Run()
	dur := time.Since(started).Truncate(time.Millisecond)
	if err != nil {
		log.Error("claude exited with error", "err", err, "duration", dur)
		return
	}
	log.Info("claude finished", "duration", dur)
}

// buildPrompt 构造给 claude 的指令。只塞 mention 索引,让 claude 自己用 MCP tool
// 拉详情(假设用户已配好 Synapse MCP server)。
func buildPrompt(mentionData string) string {
	return fmt.Sprintf(`你是 Synapse 协作系统里某个用户的 local agent,刚收到一条事件:你被 @ 了。

事件 payload(JSON):
%s

请按以下流程处理:
1. 调 list_my_mentions 拿这条 mention 的完整内容(用 payload 里的 message_id 定位)
2. 必要时调 get_task_context / list_recent_messages 补足上下文
3. 判断需要怎么回应:
   - 单纯被提到 → post_message 简短回应
   - 任务派发 → claim_task → 处理 → submit_result
   - 不需要立即响应 → 直接退出
4. 简洁说一下你做了什么,然后退出。

注意:你的回复会发到公共 channel,所有成员可见 —— 措辞要专业。
`, mentionData)
}
