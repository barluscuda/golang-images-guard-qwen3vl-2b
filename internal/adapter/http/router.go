package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func NewRouter(handler *Handler, log *zap.Logger, ready func(context.Context) error) (*gin.Engine, error) {
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	r.Use(func(c *gin.Context) {
		id, err := domain.NewID()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal_error", "Request could not be initialized.")
			return
		}
		c.Header("X-Request-ID", id)
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				// Avoid dumping request headers, bodies or panic values.
				respondError(c, http.StatusInternalServerError, "internal_error", "Request failed.")
				log.Error("request panic", zap.String("request_id", id))
			}
			log.Info("request", zap.String("request_id", id), zap.String("method", c.Request.Method),
				zap.String("route", c.FullPath()), zap.Int("status", c.Writer.Status()), zap.Duration("duration", time.Since(start)))
		}()
		c.Next()
	})
	r.POST("/v1/images", handler.Upload)
	r.GET("/v1/images/:image_id", handler.Get)
	r.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/health/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			respondError(c, http.StatusServiceUnavailable, "not_ready", "Database is unavailable.")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.NoRoute(func(c *gin.Context) { respondError(c, http.StatusNotFound, "not_found", "Route not found.") })
	r.HandleMethodNotAllowed = true
	r.NoMethod(func(c *gin.Context) {
		respondError(c, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
	})
	return r, nil
}
