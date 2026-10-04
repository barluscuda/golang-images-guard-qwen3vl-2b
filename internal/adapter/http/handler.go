package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/service"
	"github.com/gin-gonic/gin"
)

type UploadLimits struct {
	MaxBytes                                 int64
	MaxLongSide, MaxShortSide, MaxConcurrent int
}

type Handler struct {
	service *service.Guard
	limits  UploadLimits
	uploads chan struct{}
}

func NewHandler(service *service.Guard, limits UploadLimits) *Handler {
	return &Handler{service: service, limits: limits, uploads: make(chan struct{}, limits.MaxConcurrent)}
}

func (h *Handler) Upload(c *gin.Context) {
	select {
	case h.uploads <- struct{}{}:
		defer func() { <-h.uploads }()
	default:
		c.Header("Retry-After", "1")
		respondError(c, http.StatusServiceUnavailable, "busy", "Upload capacity is full.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.limits.MaxBytes+(64<<10))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_upload", "Expected multipart/form-data with one image field.")
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		uploadReadError(c, err)
		return
	}
	defer part.Close()
	if part.FormName() != "image" || part.FileName() == "" {
		respondError(c, http.StatusBadRequest, "invalid_upload", "Exactly one file named image is required.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, h.limits.MaxBytes+1))
	if err != nil {
		uploadReadError(c, err)
		return
	}
	if int64(len(data)) > h.limits.MaxBytes {
		respondError(c, http.StatusRequestEntityTooLarge, "image_too_large", "Image exceeds the byte limit.")
		return
	}
	extra, err := reader.NextPart()
	if err != io.EOF {
		if extra != nil {
			_ = extra.Close()
		}
		if err != nil {
			uploadReadError(c, err)
		} else {
			respondError(c, http.StatusBadRequest, "invalid_upload", "Only one image field is accepted.")
		}
		return
	}
	// Exhaust the bounded request body, including any multipart epilogue.
	if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
		uploadReadError(c, err)
		return
	}
	image, err := ValidateWebP(data, h.limits.MaxLongSide, h.limits.MaxShortSide)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotWebP):
			respondError(c, http.StatusUnsupportedMediaType, "unsupported_image", "Only static WebP images are accepted.")
		case errors.Is(err, ErrDimensions):
			respondError(c, http.StatusUnprocessableEntity, "invalid_dimensions", "Image exceeds the configured FHD dimensions.")
		default:
			respondError(c, http.StatusBadRequest, "invalid_image", "Image is corrupt or animated.")
		}
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	id, err := h.service.Upload(ctx, image)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "upload_failed", "Image could not be saved.")
		return
	}
	c.Header("Location", "/v1/images/"+id)
	c.JSON(http.StatusAccepted, gin.H{"image_id": id})
}

func (h *Handler) Get(c *gin.Context) {
	id := c.Param("image_id")
	if !domain.ValidID(id) {
		respondError(c, http.StatusBadRequest, "invalid_id", "Image ID must be a lowercase UUID.")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	image, err := h.service.Get(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		respondError(c, http.StatusNotFound, "not_found", "Image not found.")
		return
	}
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "database_unavailable", "Image info is unavailable.")
		return
	}
	var result *assessmentResponse
	if image.Result != nil {
		result = &assessmentResponse{Violation: image.Result.Violation, Severity: image.Result.Severity, Reason: image.Result.Reason}
		if image.Result.Rule != nil {
			result.Rule = &ruleResponse{ID: image.Result.Rule.ID, Name: image.Result.Rule.Name}
		}
	}
	var failure *errorResponse
	if image.Failure != nil {
		failure = &errorResponse{Code: image.Failure.Code, Message: image.Failure.Message}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, imageResponse{ID: image.ID, Status: image.Status, SizeBytes: image.SizeBytes,
		Width: image.Width, Height: image.Height, Result: result, Error: failure, CreatedAt: image.CreatedAt, UpdatedAt: image.UpdatedAt})
}

func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("image_id")
	if !domain.ValidID(id) {
		respondError(c, http.StatusBadRequest, "invalid_id", "Image ID must be a lowercase UUID.")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	err := h.service.Delete(ctx, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		respondError(c, http.StatusNotFound, "not_found", "Image not found.")
	case errors.Is(err, domain.ErrNotProcessed):
		respondError(c, http.StatusConflict, "not_processed", "Image can be deleted only after processing is completed.")
	case err != nil:
		respondError(c, http.StatusServiceUnavailable, "delete_failed", "Image could not be deleted.")
	default:
		c.Status(http.StatusNoContent)
	}
}

type ruleResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type assessmentResponse struct {
	Violation bool          `json:"violation"`
	Severity  int           `json:"severity"`
	Rule      *ruleResponse `json:"rule"`
	Reason    string        `json:"reason"`
}
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type imageResponse struct {
	ID        string              `json:"image_id"`
	Status    domain.Status       `json:"status"`
	SizeBytes int64               `json:"size_bytes"`
	Width     int                 `json:"width"`
	Height    int                 `json:"height"`
	Result    *assessmentResponse `json:"result"`
	Error     *errorResponse      `json:"error"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

func respondError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": errorResponse{Code: code, Message: message}})
}

func uploadReadError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		respondError(c, http.StatusRequestEntityTooLarge, "request_too_large", "Request exceeds the byte limit.")
		return
	}
	respondError(c, http.StatusBadRequest, "invalid_upload", "Malformed or missing image upload.")
}
