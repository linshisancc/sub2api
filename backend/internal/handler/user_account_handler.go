package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// UserAccountHandler 处理用户侧分组账号查询与单账号用量窗口查询。
//
// 权限基线复用 APIKeyService.GetAvailableGroups：用户能看到的分组等同于
// API Key 创建页 / 可用渠道页所见的分组集合（订阅 + user.CanBindGroup()）。
// 单账号用量需要账号至少属于一个可用分组，避免泄漏非授权账号。
type UserAccountHandler struct {
	apiKeyService       *service.APIKeyService
	accountRepo         service.AccountRepository
	accountUsageService *service.AccountUsageService
}

// NewUserAccountHandler creates a new UserAccountHandler.
func NewUserAccountHandler(
	apiKeyService *service.APIKeyService,
	accountRepo service.AccountRepository,
	accountUsageService *service.AccountUsageService,
) *UserAccountHandler {
	return &UserAccountHandler{
		apiKeyService:       apiKeyService,
		accountRepo:         accountRepo,
		accountUsageService: accountUsageService,
	}
}

// userAccountSummary 用户侧账号摘要 DTO。
// 仅返回前端渲染分组账号用量所需的最小字段集，
// 不包含 credentials / extra / quota 等敏感与内部字段。
type userAccountSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}

// ListGroupAccounts 列出指定分组下的活跃账号（用户视角）。
// GET /api/v1/groups/:id/accounts
func (h *UserAccountHandler) ListGroupAccounts(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	allowed, err := h.userAllowedGroupIDs(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if _, ok := allowed[groupID]; !ok {
		response.Forbidden(c, "Group not accessible")
		return
	}

	accounts, err := h.accountRepo.ListByGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]userAccountSummary, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		out = append(out, userAccountSummary{
			ID:       a.ID,
			Name:     a.Name,
			Platform: a.Platform,
			Type:     a.Type,
			Status:   a.Status,
		})
	}
	response.Success(c, out)
}

// GetAccountUsage 返回单个账号的实时用量窗口数据（用户视角）。
// GET /api/v1/accounts/:id/usage?source=passive|active
//
// 鉴权：账号必须至少属于当前用户可用分组之一，否则 403。
func (h *UserAccountHandler) GetAccountUsage(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	account, err := h.accountRepo.GetByID(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	allowed, err := h.userAllowedGroupIDs(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !anyGroupAllowed(account.GroupIDs, allowed) {
		response.Forbidden(c, "Account not accessible")
		return
	}

	source := c.DefaultQuery("source", "active")
	var usage *service.UsageInfo
	if source == "passive" {
		usage, err = h.accountUsageService.GetPassiveUsage(c.Request.Context(), accountID)
	} else {
		usage, err = h.accountUsageService.GetUsage(c.Request.Context(), accountID)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, usage)
}

func (h *UserAccountHandler) userAllowedGroupIDs(c *gin.Context, userID int64) (map[int64]struct{}, error) {
	groups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), userID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(groups))
	for i := range groups {
		out[groups[i].ID] = struct{}{}
	}
	return out, nil
}

func anyGroupAllowed(groupIDs []int64, allowed map[int64]struct{}) bool {
	for _, gid := range groupIDs {
		if _, ok := allowed[gid]; ok {
			return true
		}
	}
	return false
}
