package services

// 本地进程运行时：不依赖 Docker，直接用宿主机上的 sing-box 二进制管理实例。
// 运行模式由 SANDBOX_RUNTIME / RUNTIME 环境变量控制：
//   docker —— 强制走容器（原来的行为）
//   local  —— 强制走本地进程
//   未设置 —— auto：/var/run/docker.sock 存在就用 docker，否则用 local
//
// 与容器模式的目录约定保持一致：
//   默认实例配置：DATA_DIR/singbox/config.json
//   命名实例配置：DATA_DIR/singbox/configs/<name>/config.json
// 新增的运行时文件：
//   PID：DATA_DIR/run/<name>.pid
//   日志：DATA_DIR/logs/<name>.log

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	localDefaultName = "default"
	logMaxBytes      = 8 * 1024 * 1024
)

// LocalRuntime 本地进程运行时。
type LocalRuntime struct {
	bin      string
	dataDir  string
	runDir   string
	logDir   string
	stateDir string
}

var (
	rtOnce  sync.Once
	localRT *LocalRuntime
)

// RuntimeMode 返回当前运行模式：docker 或 local。
func RuntimeMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SANDBOX_RUNTIME")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("RUNTIME")))
	}
	if mode == "docker" || mode == "local" {
		return mode
	}
	// auto：有 docker socket 优先用容器，保持原有行为
	if st, err := os.Stat("/var/run/docker.sock"); err == nil && !st.IsDir() {
		return "docker"
	}
	return "local"
}

func singboxBinary() string {
	if b := strings.TrimSpace(os.Getenv("SINGBOX_BIN")); b != "" {
		return b
	}
	// 常见安装位置
	for _, p := range []string{"/usr/local/bin/sing-box", "/usr/bin/sing-box", "/opt/singbox/sing-box"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return "/usr/local/bin/sing-box"
}

// GetLocalRuntime 返回单例。
func GetLocalRuntime() (*LocalRuntime, error) {
	rtOnce.Do(func() {
		localRT = &LocalRuntime{
			bin:      singboxBinary(),
			dataDir:  baseDir,
			runDir:   filepath.Join(baseDir, "run"),
			logDir:   filepath.Join(baseDir, "logs"),
			stateDir: filepath.Join(baseDir, "state"),
		}
	})
	for _, d := range []string{localRT.runDir, localRT.logDir, localRT.stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("创建目录 %s 失败: %w", d, err)
		}
	}
	return localRT, nil
}

// EnsureBinary 校验 sing-box 二进制可用。
func (l *LocalRuntime) EnsureBinary() error {
	st, err := os.Stat(l.bin)
	if err != nil {
		return fmt.Errorf("未找到 sing-box 二进制 %s；请安装或设置 SINGBOX_BIN。下载：https://github.com/SagerNet/sing-box/releases", l.bin)
	}
	if st.IsDir() {
		return fmt.Errorf("SINGBOX_BIN 指向目录: %s", l.bin)
	}
	if st.Mode()&0o111 == 0 {
		return fmt.Errorf("sing-box 不可执行: %s（chmod +x 后重试）", l.bin)
	}
	return nil
}

func (l *LocalRuntime) pidPath(name string) string { return filepath.Join(l.runDir, name+".pid") }
func (l *LocalRuntime) logPath(name string) string { return filepath.Join(l.logDir, name+".log") }

// ReadPID 读取实例 PID，返回 0 表示未运行。
func (l *LocalRuntime) ReadPID(name string) int {
	b, err := os.ReadFile(l.pidPath(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// processAlive 判断 PID 存活且确实是 sing-box（避免 PID 复用误判）。
func (l *LocalRuntime) processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// 没有 /proc 时退化为仅看信号探测结果
		return true
	}
	return strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), "sing-box")
}

// rotateLog 日志超过上限时截断，避免无限增长。
func (l *LocalRuntime) rotateLog(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < logMaxBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}

// Run 启动一个实例，返回 "local:<pid>"。
func (l *LocalRuntime) Run(cfgDir, name string) (string, error) {
	if err := l.EnsureBinary(); err != nil {
		return "", err
	}
	configPath := filepath.Join(cfgDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return "", fmt.Errorf("config file not found: %s", configPath)
	}

	// 已在运行则先停掉，保证配置是最新的
	if pid := l.ReadPID(name); pid > 0 && l.processAlive(pid) {
		if err := l.stopPID(name, pid); err != nil {
			return "", err
		}
	}

	l.rotateLog(l.logPath(name))
	logFile, err := os.OpenFile(l.logPath(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("打开日志失败: %w", err)
	}
	defer logFile.Close()

	_, _ = logFile.WriteString(fmt.Sprintf("\n===== start %s at %s =====\n", name, time.Now().Format("2006-01-02 15:04:05")))

	cmd := exec.Command(l.bin, "-D", l.stateDir, "-C", cfgDir+string(os.PathSeparator), "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// 兼容 1.12+：旧配置常缺 route.default_domain_resolver，新版默认 FATAL。
	// 允许其继续运行，避免升级 sing-box 后整批实例起不来。
	cmd.Env = append(os.Environ(), "ENABLE_DEPRECATED_MISSING_DOMAIN_RESOLVER=true")
	// 脱离当前进程组，UI 重启不会带走 sing-box
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 sing-box 失败: %w", err)
	}
	pid := cmd.Process.Pid
	// 子进程已通过 Setsid 独立成组，但仍是本进程的子进程：
	// 必须有人 wait() 回收，否则退出后留下 <defunct> 僵尸。
	go func(c *exec.Cmd) { _ = c.Wait() }(cmd)

	if err := os.WriteFile(l.pidPath(name), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return "", fmt.Errorf("写 PID 失败: %w", err)
	}

	// 给一点启动时间，尽早发现配置错误
	time.Sleep(600 * time.Millisecond)
	if !l.processAlive(pid) {
		tail := l.tailLog(name, 30)
		_ = os.Remove(l.pidPath(name))
		return "", fmt.Errorf("sing-box 启动后立即退出，日志末尾:\n%s", tail)
	}

	log.Printf("local runtime: 实例 %s 已启动 pid=%d", name, pid)
	return fmt.Sprintf("local:%d", pid), nil
}

// Stop 停止实例（幂等）。
func (l *LocalRuntime) Stop(name string) error {
	pid := l.ReadPID(name)
	if pid == 0 || !l.processAlive(pid) {
		_ = os.Remove(l.pidPath(name))
		return nil
	}
	if err := l.stopPID(name, pid); err != nil {
		return err
	}
	log.Printf("local runtime: 实例 %s 已停止 pid=%d", name, pid)
	return nil
}

func (l *LocalRuntime) stopPID(name string, pid int) error {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !l.processAlive(pid) {
			_ = os.Remove(l.pidPath(name))
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	_ = os.Remove(l.pidPath(name))
	if l.processAlive(pid) {
		return fmt.Errorf("实例 %s (pid=%d) 无法停止", name, pid)
	}
	return nil
}

// Status 返回实例是否在运行及标识。
func (l *LocalRuntime) Status(name string) (bool, string) {
	pid := l.ReadPID(name)
	if pid > 0 && l.processAlive(pid) {
		return true, fmt.Sprintf("local:%d", pid)
	}
	if pid > 0 {
		_ = os.Remove(l.pidPath(name))
	}
	return false, ""
}

// Logs 返回日志末尾 tail 行。
func (l *LocalRuntime) Logs(name string, tail int) (string, error) {
	b, err := os.ReadFile(l.logPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "（暂无日志）", nil
		}
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	// sing-box 日志带 ANSI 颜色码，前端日志面板里会显示成乱码
	return stripAnsi(strings.Join(lines, "\n")), nil
}

func (l *LocalRuntime) tailLog(name string, n int) string {
	s, err := l.Logs(name, n)
	if err != nil {
		return ""
	}
	return s
}

// Version 返回 sing-box 版本字符串。
func (l *LocalRuntime) Version() (string, error) {
	if err := l.EnsureBinary(); err != nil {
		return "", err
	}
	out, err := exec.Command(l.bin, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("执行 sing-box version 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	line := strings.TrimSpace(string(out))
	if i := strings.Index(line, "\n"); i >= 0 {
		line = line[:i]
	}
	return line, nil
}

// Check 用 `sing-box check` 校验配置目录。
func (l *LocalRuntime) Check(cfgDir string) (bool, string, error) {
	if err := l.EnsureBinary(); err != nil {
		return false, "", err
	}
	out, err := exec.Command(l.bin, "check", "-C", cfgDir+string(os.PathSeparator)).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return false, s, nil
	}
	return true, s, nil
}

// Names 返回所有已存在的实例名（default + configs 下的目录）。
func (l *LocalRuntime) Names() []string {
	names := []string{localDefaultName}
	configsDir := filepath.Join(singboxDir, "configs")
	entries, err := os.ReadDir(configsDir)
	if err != nil {
		return names
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(configsDir, e.Name(), "config.json")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}
