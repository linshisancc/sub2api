//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type loginBruteforceMiddlewareSettingRepo struct {
	values map[string]string
}

func (r *loginBruteforceMiddlewareSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get")
}

func (r *loginBruteforceMiddlewareSettingRepo) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue")
}

func (r *loginBruteforceMiddlewareSettingRepo) Set(context.Context, string, string) error {
	panic("unexpected Set")
}

func (r *loginBruteforceMiddlewareSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = r.values[key]
	}
	return values, nil
}

func (r *loginBruteforceMiddlewareSettingRepo) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple")
}

func (r *loginBruteforceMiddlewareSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll")
}

func (r *loginBruteforceMiddlewareSettingRepo) Delete(context.Context, string) error {
	panic("unexpected Delete")
}

var _ service.SettingRepository = (*loginBruteforceMiddlewareSettingRepo)(nil)

func TestLoginBruteforceTrackerRecordsCanceledRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })

	settingRepo := &loginBruteforceMiddlewareSettingRepo{values: map[string]string{
		service.SettingKeyFeishuLoginBruteforceAutobanEnabled: "true",
		service.SettingKeyLoginBruteforceMaxFailures:          "1",
		service.SettingKeyLoginBruteforceWindowMinutes:        "5",
		service.SettingKeyLoginBruteforceBanMinutes:           "60",
	}}
	bruteforceService := service.NewLoginBruteforceService(settingRepo, redisClient, nil)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.POST("/login", gin.HandlerFunc(NewLoginBruteforceTrackerMiddleware(bruteforceService)), func(c *gin.Context) {
		c.Status(http.StatusUnauthorized)
	})

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodPost, "/login", nil).WithContext(requestContext)
	req.RemoteAddr = "203.0.113.10:54321"
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	require.Equal(t, http.StatusUnauthorized, resp.Code)
	banned, _, err := bruteforceService.IsBanned(context.Background(), "203.0.113.10")
	require.NoError(t, err)
	require.True(t, banned, "canceled client request must still be counted and banned")
}
