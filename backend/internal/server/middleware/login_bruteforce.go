package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const loginBruteforceRecordTimeout = 3 * time.Second

// LoginBruteforceTrackerMiddleware 登录失败计数中间件类型（用于 wire 注入区分）。
type LoginBruteforceTrackerMiddleware gin.HandlerFunc

// IPBanCheckMiddleware 全局 IP 封禁检查中间件类型（用于 wire 注入区分）。
type IPBanCheckMiddleware gin.HandlerFunc

// NewLoginBruteforceTrackerMiddleware 创建登录失败计数中间件。
// 仅挂载在 POST /api/v1/auth/login：请求完成后检查状态码，401 时计入该 IP 的失败计数，
// 达到阈值即自动封禁并触发飞书告警（由 LoginBruteforceService 内部处理）。
// 与审计日志中间件（audit_log.go）刻意分离，避免耦合其请求体捕获/脱敏逻辑。
func NewLoginBruteforceTrackerMiddleware(loginBruteforceService *service.LoginBruteforceService) LoginBruteforceTrackerMiddleware {
	return LoginBruteforceTrackerMiddleware(func(c *gin.Context) {
		c.Next()

		if c.Writer.Status() != http.StatusUnauthorized {
			return
		}
		clientIP := SecurityClientIP(c)
		if clientIP == "" {
			return
		}
		// 保留请求值（包括统一解析的安全客户端 IP），但不允许客户端断开取消安全计数。
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), loginBruteforceRecordTimeout)
		defer cancel()
		if _, _, err := loginBruteforceService.RecordFailure(recordCtx, clientIP); err != nil {
			// 计数失败不影响登录响应本身，仅记录到服务内部日志（fail-open）。
			_ = err
		}
	})
}

// NewIPBanCheckMiddleware 创建全局 IP 封禁检查中间件。
// 挂载在中间件链最前面，命中封禁的 IP 直接 403，不再进入后续路由（含网关转发接口）。
// Redis 查询失败时 fail-open：绝不能因为 Redis 抖动把全站流量拦掉。
func NewIPBanCheckMiddleware(loginBruteforceService *service.LoginBruteforceService) IPBanCheckMiddleware {
	return IPBanCheckMiddleware(func(c *gin.Context) {
		clientIP := SecurityClientIP(c)
		if clientIP == "" {
			c.Next()
			return
		}

		banned, _, err := loginBruteforceService.IsBanned(c.Request.Context(), clientIP)
		if err != nil {
			// fail-open
			c.Next()
			return
		}
		if banned {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "IP_BANNED",
					"message": "该 IP 因异常登录行为已被临时封禁",
				},
			})
			return
		}
		c.Next()
	})
}
