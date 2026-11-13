package handlers

import (
	"net/http"
	"os"
	"path/filepath"

	"singbox-config-service/services"

	"github.com/gin-gonic/gin"
)

// GET /api/gateway/status
func GetGatewayStatus(c *gin.Context) {
	c.JSON(http.StatusOK, services.GetGateway().Status())
}

// GET /api/gateway/config
func GetGatewayConfig(c *gin.Context) {
	c.JSON(http.StatusOK, services.GetGateway().Config())
}

// POST /api/gateway/config —— 只保存不应用
func SaveGatewayConfig(c *gin.Context) {
	var cfg services.GatewayConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if err := services.GetGateway().UpdateConfig(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "config": services.GetGateway().Config()})
}

// GET /api/gateway/interfaces
func ListGatewayInterfaces(c *gin.Context) {
	ifaces, err := services.ListInterfaces()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	def, _ := services.DetectDefaultInterface()
	c.JSON(http.StatusOK, gin.H{"default": def, "interfaces": ifaces})
}

// GET /api/gateway/rules —— 预览将要加载的 nft 文本
func PreviewGatewayRules(c *gin.Context) {
	text, err := services.GetGateway().RenderNft()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"nft": text})
}

// POST /api/gateway/apply —— 启用并应用；可选 body 覆盖配置
func ApplyGateway(c *gin.Context) {
	svc := services.GetGateway()
	cfg := svc.Config()
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
			return
		}
	}
	if err := svc.Start(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "status": svc.Status()})
		return
	}

	// 确保默认 sing-box 配置里有 tproxy 入站
	restartRequired := false
	cfgPath := filepath.Join(dataDirForGateway(), "singbox", "config.json")
	if _, err := os.Stat(cfgPath); err == nil {
		changed, err := services.InjectTproxyInbound(cfgPath, cfg.TProxyPort)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"ok":               true,
				"restart_required": false,
				"warning":          "tproxy 入站注入失败: " + err.Error(),
				"status":           svc.Status(),
			})
			return
		}
		restartRequired = changed
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"restart_required": restartRequired,
		"status":           svc.Status(),
	})
}

// POST /api/gateway/stop
func StopGateway(c *gin.Context) {
	if err := services.GetGateway().Stop(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": services.GetGateway().Status()})
}

// POST /api/gateway/heal
func HealGateway(c *gin.Context) {
	if err := services.GetGateway().Heal(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "status": services.GetGateway().Status()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": services.GetGateway().Status()})
}

func dataDirForGateway() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return wd
}
