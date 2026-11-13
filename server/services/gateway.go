// Package services —— 网关模式（tproxy）编排。
//
// 设计要点（与 smbox 的教训对齐）：
//  1. 只维护独立表 `table ip smbox`，绝不 `flush ruleset`，避免清掉别人的规则；
//  2. nft 卸载用 `nft delete table`（幂等），加载用临时文件 + `nft -f`；
//  3. tproxy 必须同时具备 nft 规则 + ip rule(fwmark) + ip route(local)，三者缺一即不通；
//  4. 网关接口允许手工覆盖，探测失败时不会写死一个错误接口；
//  5. 自愈循环按固定间隔检查三要素，缺失即重建。
//
// 加锁纪律：cfgMu 只保护配置字段，stMu 只保护运行状态字段；
// 任何持锁路径都不得再调用持同一把锁的方法（Go 的 Mutex 不可重入）。
package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	GatewayTableName = "smbox"
	gatewayFileName  = "gateway.json"
)

// GatewayConfig 网关模式配置，持久化在 DATA_DIR/gateway.json。
type GatewayConfig struct {
	Enabled      bool     `json:"enabled"`
	Interface    string   `json:"interface"`     // 空 = 自动探测默认路由接口
	LanCIDR      string   `json:"lan_cidr"`      // 局域网网段，用于 masquerade
	TProxyPort   int      `json:"tproxy_port"`   // tproxy 监听端口
	Mark         int      `json:"mark"`          // fwmark，默认 1
	Table        int      `json:"table"`         // 路由表号，默认 100
	ExcludeCIDRs []string `json:"exclude_cidrs"` // 直连（不代理）的网段
	ExcludePorts []int    `json:"exclude_ports"` // 不代理的端口
	SelfHeal     bool     `json:"self_heal"`
	HealInterval int      `json:"heal_interval"` // 秒
	Masquerade   bool     `json:"masquerade"`
}

func DefaultGatewayConfig() *GatewayConfig {
	return &GatewayConfig{
		Enabled:      false,
		Interface:    "",
		LanCIDR:      "192.168.0.0/16",
		TProxyPort:   9888,
		Mark:         1,
		Table:        100,
		ExcludeCIDRs: []string{"10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "240.0.0.0/4"},
		ExcludePorts: []int{53, 123},
		SelfHeal:     true,
		HealInterval: 20,
		Masquerade:   true,
	}
}

// GatewayStatus 是给前端看的实时状态。
type GatewayStatus struct {
	Enabled    bool   `json:"enabled"`
	Interface  string `json:"interface"`
	AutoDetect bool   `json:"auto_detect"`
	TableName  string `json:"table_name"`
	NftOK      bool   `json:"nft_ok"`
	IPRuleOK   bool   `json:"ip_rule_ok"`
	IPRouteOK  bool   `json:"ip_route_ok"`
	Healthy    bool   `json:"healthy"`
	LastApply  string `json:"last_apply"`
	LastError  string `json:"last_error"`
	HealCount  int    `json:"heal_count"`
	NftRules   string `json:"nft_rules"`
	Marker     string `json:"marker"`
	RuleTable  int    `json:"rule_table"`
	TProxyPort int    `json:"tproxy_port"`
	LanCIDR    string `json:"lan_cidr"`
	Now        string `json:"now"`
}

// GatewayService 单例。
type GatewayService struct {
	cfgMu sync.RWMutex
	cfg   *GatewayConfig

	stMu      sync.Mutex
	lastApply time.Time
	lastErr   string
	healCount int

	cfgPath  string
	stopCh   chan struct{}
	startedM sync.Mutex
	started  bool
}

var (
	gwOnce sync.Once
	gwSvc  *GatewayService
)

func gatewayDataDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/home/data"
}

// GatewayDataDir 导出数据目录，供 handler 定位默认 sing-box 配置。
func GatewayDataDir() string { return gatewayDataDir() }

// GetGateway 返回单例。
func GetGateway() *GatewayService {
	gwOnce.Do(func() {
		gwSvc = &GatewayService{
			cfgPath: filepath.Join(gatewayDataDir(), gatewayFileName),
			cfg:     DefaultGatewayConfig(),
			stopCh:  make(chan struct{}),
		}
		gwSvc.load()
	})
	return gwSvc
}

func (g *GatewayService) load() {
	b, err := os.ReadFile(g.cfgPath)
	if err != nil {
		return
	}
	var c GatewayConfig
	if err := json.Unmarshal(b, &c); err != nil {
		log.Printf("gateway: 解析 %s 失败: %v", g.cfgPath, err)
		return
	}
	normalize(&c)
	g.cfg = &c
}

func normalize(c *GatewayConfig) {
	if c.Mark == 0 {
		c.Mark = 1
	}
	if c.Table == 0 {
		c.Table = 100
	}
	if c.TProxyPort == 0 {
		c.TProxyPort = 9888
	}
	if c.HealInterval <= 0 {
		c.HealInterval = 20
	}
}

// snapshot 取配置快照（唯一允许的读配置方式）。
func (g *GatewayService) snapshot() GatewayConfig {
	g.cfgMu.RLock()
	defer g.cfgMu.RUnlock()
	return *g.cfg
}

// Config 返回当前配置副本。
func (g *GatewayService) Config() GatewayConfig { return g.snapshot() }

func (g *GatewayService) setErr(msg string) {
	g.stMu.Lock()
	g.lastErr = msg
	g.stMu.Unlock()
}

// UpdateConfig 保存配置。
func (g *GatewayService) UpdateConfig(c GatewayConfig) error {
	normalize(&c)
	g.cfgMu.Lock()
	g.cfg = &c
	g.cfgMu.Unlock()
	return g.persist(c)
}

func (g *GatewayService) persist(c GatewayConfig) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.cfgPath)
}

// ---- 接口探测 ----

type InterfaceInfo struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	Addresses string `json:"addresses"`
}

var defaultDevRe = regexp.MustCompile(`dev\s+(\S+)`)

// DetectDefaultInterface 从 `ip route show default` 取默认出口接口。
func DetectDefaultInterface() (string, error) {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("没有默认路由")
	}
	m := defaultDevRe.FindStringSubmatch(line)
	if len(m) < 2 {
		return "", fmt.Errorf("无法从默认路由解析接口: %s", line)
	}
	return m[1], nil
}

// ListInterfaces 列出接口并标记默认路由所在者。
func ListInterfaces() ([]InterfaceInfo, error) {
	def, _ := DetectDefaultInterface()
	out, err := exec.Command("ip", "-o", "link", "show").Output()
	if err != nil {
		return nil, err
	}
	var res []InterfaceInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, ": ", 2)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
		if i := strings.Index(name, ":"); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		addr, _ := exec.Command("ip", "-o", "addr", "show", "dev", name).Output()
		res = append(res, InterfaceInfo{
			Name:      name,
			Default:   name == def,
			Addresses: strings.TrimSpace(string(addr)),
		})
	}
	return res, nil
}

// resolveIface 纯函数：手工优先，否则探测。
func resolveIface(c GatewayConfig) (string, bool, error) {
	if strings.TrimSpace(c.Interface) != "" {
		return strings.TrimSpace(c.Interface), false, nil
	}
	d, err := DetectDefaultInterface()
	return d, true, err
}

// ResolveInterface 返回最终使用的接口（手工优先）。
func (g *GatewayService) ResolveInterface() (string, bool, error) {
	return resolveIface(g.snapshot())
}

// ---- nft 规则渲染 ----

// RenderNftFor 纯函数：根据配置生成独立表的完整规则文本。绝不含 flush ruleset。
func RenderNftFor(c GatewayConfig) (string, error) {
	iface, _, err := resolveIface(c)
	if err != nil {
		return "", fmt.Errorf("无法确定网关接口: %w", err)
	}
	set := "lan_v4"
	var b strings.Builder
	b.WriteString("table ip " + GatewayTableName + " {\n")
	b.WriteString("\tset " + set + " {\n\t\ttype ipv4_addr\n\t\tflags interval\n")
	b.WriteString("\t\telements = { " + strings.Join(c.ExcludeCIDRs, ", ") + " }\n\t}\n")

	if len(c.ExcludePorts) > 0 {
		ports := make([]string, 0, len(c.ExcludePorts))
		for _, p := range c.ExcludePorts {
			ports = append(ports, fmt.Sprintf("%d", p))
		}
		b.WriteString("\tset excluded_ports {\n\t\ttype inet_service\n\t\tflags interval\n")
		b.WriteString("\t\telements = { " + strings.Join(ports, ", ") + " }\n\t}\n")
	}

	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype filter hook prerouting priority mangle; policy accept;\n")
	b.WriteString("\t\tfib daddr type { unspec, local, anycast, multicast } return\n")
	b.WriteString("\t\tip daddr @" + set + " return\n")
	if len(c.ExcludePorts) > 0 {
		b.WriteString("\t\tmeta l4proto { tcp, udp } th dport @excluded_ports return\n")
	}
	b.WriteString(fmt.Sprintf("\t\tmeta l4proto { tcp, udp } ct direction original meta mark set 0x%x tproxy ip to :%d accept\n", c.Mark, c.TProxyPort))
	b.WriteString("\t}\n")

	if c.Masquerade && strings.TrimSpace(c.LanCIDR) != "" {
		b.WriteString("\tchain postrouting {\n")
		b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
		b.WriteString(fmt.Sprintf("\t\tip saddr %s oifname \"%s\" masquerade\n", c.LanCIDR, iface))
		b.WriteString("\t}\n")
	}

	b.WriteString("}\n")
	return b.String(), nil
}

// RenderNft 预览。
func (g *GatewayService) RenderNft() (string, error) {
	return RenderNftFor(g.snapshot())
}

// ---- 应用/卸载 ----

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %v (%s)", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func ipRuleExists(mark int, table int) bool {
	out, err := exec.Command("ip", "rule", "show").Output()
	if err != nil {
		return false
	}
	m := fmt.Sprintf("fwmark 0x%x", mark)
	t := fmt.Sprintf("lookup %d", table)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, m) && strings.Contains(line, t) {
			return true
		}
	}
	return false
}

func ipRouteExists(table int) bool {
	out, err := exec.Command("ip", "route", "show", "table", fmt.Sprintf("%d", table)).Output()
	if err != nil {
		return false
	}
	// 内核会把 0.0.0.0/0 显示成 default，两种形式都要认
	s := string(out)
	return strings.Contains(s, "local default") || strings.Contains(s, "local 0.0.0.0/0")
}

func applyIPRule(c GatewayConfig) error {
	if !ipRuleExists(c.Mark, c.Table) {
		if _, err := runCmd("ip", "rule", "add", "fwmark", fmt.Sprintf("0x%x", c.Mark), "lookup", fmt.Sprintf("%d", c.Table)); err != nil {
			return fmt.Errorf("ip rule 添加失败: %v", err)
		}
	}
	if !ipRouteExists(c.Table) {
		if _, err := runCmd("ip", "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprintf("%d", c.Table)); err != nil {
			return fmt.Errorf("ip route 添加失败: %v", err)
		}
	}
	return nil
}

// Apply 应用网关规则：nft 表 + ip rule + ip route。全程不持 cfgMu。
func (g *GatewayService) Apply() error {
	c := g.snapshot()

	text, err := RenderNftFor(c)
	if err != nil {
		g.setErr(err.Error())
		return err
	}
	nftFile := filepath.Join(gatewayDataDir(), "gateway.nft")
	if err := os.WriteFile(nftFile, []byte(text), 0o644); err != nil {
		g.setErr(err.Error())
		return err
	}

	// 只在自身表上做删除，不触碰其他表
	runCmd("nft", "delete", "table", "ip", GatewayTableName)
	if out, err := runCmd("nft", "-f", nftFile); err != nil {
		g.setErr(err.Error())
		return fmt.Errorf("加载 nft 失败: %v (%s)", err, out)
	}

	if err := applyIPRule(c); err != nil {
		g.setErr(err.Error())
		return err
	}

	g.stMu.Lock()
	g.lastApply = time.Now()
	g.lastErr = ""
	g.stMu.Unlock()

	iface, _, _ := resolveIface(c)
	log.Printf("gateway: 已应用 tproxy 规则（接口=%s 端口=%d 表=ip %s）", iface, c.TProxyPort, GatewayTableName)
	return nil
}

// Remove 卸载网关规则（幂等，仅动自己的资源）。
func (g *GatewayService) Remove() error {
	c := g.snapshot()
	runCmd("nft", "delete", "table", "ip", GatewayTableName)
	runCmd("ip", "rule", "del", "fwmark", fmt.Sprintf("0x%x", c.Mark), "lookup", fmt.Sprintf("%d", c.Table))
	runCmd("ip", "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprintf("%d", c.Table))
	g.stMu.Lock()
	g.lastApply = time.Time{}
	g.stMu.Unlock()
	log.Printf("gateway: 已卸载 tproxy 规则")
	return nil
}

// Check 返回三要素存活情况。
func (g *GatewayService) Check() (nftOK, ruleOK, routeOK bool) {
	c := g.snapshot()
	if _, err := exec.Command("nft", "list", "table", "ip", GatewayTableName).Output(); err == nil {
		nftOK = true
	}
	ruleOK = ipRuleExists(c.Mark, c.Table)
	routeOK = ipRouteExists(c.Table)
	return
}

// Status 汇总状态。
func (g *GatewayService) Status() GatewayStatus {
	c := g.snapshot()
	iface, auto, _ := resolveIface(c)
	nftOK, ruleOK, routeOK := g.Check()

	g.stMu.Lock()
	last := g.lastApply
	lastErr := g.lastErr
	heals := g.healCount
	g.stMu.Unlock()

	st := GatewayStatus{
		Enabled:    c.Enabled,
		Interface:  iface,
		AutoDetect: auto,
		TableName:  "ip " + GatewayTableName,
		NftOK:      nftOK,
		IPRuleOK:   ruleOK,
		IPRouteOK:  routeOK,
		Healthy:    nftOK && ruleOK && routeOK,
		LastError:  lastErr,
		HealCount:  heals,
		Marker:     fmt.Sprintf("0x%x", c.Mark),
		RuleTable:  c.Table,
		TProxyPort: c.TProxyPort,
		LanCIDR:    c.LanCIDR,
		Now:        time.Now().Format("2006-01-02 15:04:05"),
	}
	if !last.IsZero() {
		st.LastApply = last.Format("2006-01-02 15:04:05")
	}
	if nftOK {
		out, _ := exec.Command("nft", "list", "table", "ip", GatewayTableName).Output()
		st.NftRules = string(out)
	}
	return st
}

// Heal 执行一次自愈：缺失项重建。
func (g *GatewayService) Heal() error {
	c := g.snapshot()
	if !c.Enabled {
		return nil
	}
	nftOK, ruleOK, routeOK := g.Check()
	if nftOK && ruleOK && routeOK {
		return nil
	}
	log.Printf("gateway: 自愈触发 nft=%v rule=%v route=%v", nftOK, ruleOK, routeOK)
	if err := g.Apply(); err != nil {
		return err
	}
	g.stMu.Lock()
	g.healCount++
	g.stMu.Unlock()
	return nil
}

// StartSelfHeal 启动后台自愈循环。
func (g *GatewayService) StartSelfHeal() {
	g.startedM.Lock()
	if g.started {
		g.startedM.Unlock()
		return
	}
	g.started = true
	g.startedM.Unlock()

	go func() {
		for {
			interval := g.snapshot().HealInterval
			if interval <= 0 {
				interval = 20
			}
			select {
			case <-g.stopCh:
				return
			case <-time.After(time.Duration(interval) * time.Second):
			}
			c := g.snapshot()
			if !c.Enabled || !c.SelfHeal {
				continue
			}
			if err := g.Heal(); err != nil {
				log.Printf("gateway: 自愈失败: %v", err)
			}
		}
	}()
	log.Println("gateway: 自愈循环已启动")
}

// Start 启用网关模式：写配置 + 应用 + 起自愈。
func (g *GatewayService) Start(c GatewayConfig) error {
	c.Enabled = true
	if err := g.UpdateConfig(c); err != nil {
		return err
	}
	if err := g.Apply(); err != nil {
		return err
	}
	g.StartSelfHeal()
	return nil
}

// Stop 停用网关模式。
func (g *GatewayService) Stop() error {
	c := g.snapshot()
	c.Enabled = false
	if err := g.UpdateConfig(c); err != nil {
		return err
	}
	return g.Remove()
}

// ---- 给 sing-box 配置注入 tproxy 入站 ----

// InjectTproxyInbound 在默认 sing-box 配置中确保存在 tproxy 入站。
// 返回是否发生了修改。写前留 .bak，写后调用方负责重启容器。
func InjectTproxyInbound(configPath string, port int) (bool, error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return false, fmt.Errorf("配置不是合法 JSON: %w", err)
	}
	inbounds, _ := cfg["inbounds"].([]any)
	for _, in := range inbounds {
		m, ok := in.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "tproxy" {
			if lp, ok := m["listen_port"].(float64); ok && int(lp) == port {
				return false, nil
			}
		}
	}
	cfg["inbounds"] = append(inbounds, map[string]any{
		"type":        "tproxy",
		"tag":         "tproxy-in",
		"listen":      "0.0.0.0",
		"listen_port": port,
		"network":     "tcp,udp",
	})
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(configPath+".bak", b, 0o644); err != nil {
		return false, err
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, configPath)
}
