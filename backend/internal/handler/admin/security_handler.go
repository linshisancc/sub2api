package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SecurityHandler 登录爆破自动封禁相关的最小化管理接口：
// 仅支持查看当前封禁 IP 列表与手动解封，不建历史记录表（详见方案文档）。
type SecurityHandler struct {
	loginBruteforceService *service.LoginBruteforceService
}

// NewSecurityHandler 创建安全管理接口处理器。
func NewSecurityHandler(loginBruteforceService *service.LoginBruteforceService) *SecurityHandler {
	return &SecurityHandler{loginBruteforceService: loginBruteforceService}
}

// ListBannedIPs 查看当前处于封禁状态的 IP 列表。
// GET /api/v1/admin/security/banned-ips
func (h *SecurityHandler) ListBannedIPs(c *gin.Context) {
	entries, err := h.loginBruteforceService.ListBanned(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if entries == nil {
		entries = []service.BannedIPEntry{}
	}
	response.Success(c, entries)
}

// UnbanIP 手动解封指定 IP。
// DELETE /api/v1/admin/security/banned-ips/:ip
func (h *SecurityHandler) UnbanIP(c *gin.Context) {
	targetIP := strings.TrimSpace(c.Param("ip"))
	if targetIP == "" {
		response.BadRequest(c, "Invalid IP")
		return
	}
	if err := h.loginBruteforceService.Unban(c.Request.Context(), targetIP); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"unbanned": targetIP})
}
