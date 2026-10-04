package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/http"
	modelapi "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/llamacpp"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/postgres"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/storage"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func integrationRepository(t *testing.T) (*Image, *postgres.Client) {
	t.Helper()
	dsn := os.Getenv("GUARD_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set GUARD_TEST_DATABASE_DSN to run PostgreSQL integration checks")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("integration DSN must be a PostgreSQL URI")
	}
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "guard_test_" + strings.ReplaceAll(id, "-", "")
	base, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := base.WithContext(ctx).Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := base.WithContext(cleanupCtx).Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		db, _ := base.DB()
		_ = db.Close()
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scopedDSN := u.String()
	// Startup must fail for an empty schema without creating any tables.
	if client, err := postgres.Open(ctx, scopedDSN, 4, 2); err == nil {
		_ = client.Close()
		t.Fatal("opened an unmigrated schema")
	}
	var tables int64
	if err := base.WithContext(ctx).Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = ?", schema).Scan(&tables).Error; err != nil || tables != 0 {
		t.Fatalf("startup changed schema: tables=%d err=%v", tables, err)
	}
	scoped, err := gorm.Open(pgdriver.Open(scopedDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db, _ := scoped.DB(); _ = db.Close() }()
	up, err := os.ReadFile("../../migrations/000001_create_images.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000001_create_images.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range [][]byte{up, down, up} {
		if err := scoped.WithContext(ctx).Exec(string(migration)).Error; err != nil {
			t.Fatalf("migration failed: %v", err)
		}
	}
	if err := scoped.WithContext(ctx).Exec("CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL); INSERT INTO schema_migrations VALUES (1, false)").Error; err != nil {
		t.Fatal(err)
	}
	client, err := postgres.Open(ctx, scopedDSN, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewImage(client.DB()), client
}

func TestPostgresWorkflow(t *testing.T) {
	repo, client := integrationRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	policy, err := domain.NewPolicy("RULE SEXUAL_CONTENT | Sexual Content\noriginal-policy-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	imageData, err := os.ReadFile("../adapter/http/testdata/landscape.webp")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(t.TempDir(), int64(len(imageData)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	guard := service.NewGuard(repo, store, nil, policy, "saved-thinking-model", service.Options{})
	gin.SetMode(gin.TestMode)
	router, err := httpapi.NewRouter(httpapi.NewHandler(guard, httpapi.UploadLimits{
		MaxBytes: int64(len(imageData)), MaxLongSide: 1920, MaxShortSide: 1080, MaxConcurrent: 2,
	}), zap.NewNop(), client.Ping)
	if err != nil {
		t.Fatal(err)
	}

	upload := func(files ...[]byte) *httptest.ResponseRecorder {
		t.Helper()
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		for _, data := range files {
			file, err := writer.CreateFormFile("image", "untrusted-filename.jpg")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/images", body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	info := func(id string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/images/"+id, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET: %d %s", response.Code, response.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	uploadID := func() string {
		t.Helper()
		response := upload(imageData)
		if response.Code != http.StatusAccepted {
			t.Fatalf("upload: %d %s", response.Code, response.Body.String())
		}
		var data struct {
			ID string `json:"image_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if response.Header().Get("Location") != "/v1/images/"+data.ID {
			t.Fatal("missing location")
		}
		return data.ID
	}

	t.Run("upload-validation", func(t *testing.T) {
		for _, tc := range []struct {
			files [][]byte
			code  int
		}{
			{nil, 400}, {[][]byte{[]byte("not WebP")}, 415},
			{[][]byte{append(append([]byte(nil), imageData...), 0)}, 413},
			{[][]byte{imageData, imageData}, 400},
		} {
			if r := upload(tc.files...); r.Code != tc.code {
				t.Fatalf("got %d want %d: %s", r.Code, tc.code, r.Body.String())
			}
		}
		var count int64
		if err := repo.db.Model(&imageRow{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rejected uploads created rows: %d %v", count, err)
		}
	})

	t.Run("async-upload-and-policy-snapshot", func(t *testing.T) {
		id := uploadID()
		if data := info(id); data["status"] != "pending" || data["result"] != nil {
			t.Fatal(data)
		}
		var sawRequest atomic.Bool
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if r.URL.Path != "/v1/chat/completions" || request["model"] != "saved-thinking-model" {
				t.Error("incorrect model request")
			}
			if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true {
				t.Error("thinking is disabled")
			}
			if request["response_format"].(map[string]any)["type"] != "json_schema" {
				t.Error("schema missing")
			}
			encoded, _ := json.Marshal(request["messages"])
			if !bytes.Contains(encoded, []byte("original-policy-snapshot")) || !bytes.Contains(encoded, []byte("data:image/png;base64,")) {
				t.Error("image or saved policy missing")
			}
			sawRequest.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{
				"role": "assistant", "reasoning_content": "private thinking", "content": `{"violation":false,"severity":0,"rule":null,"reason":"No violation."}`,
			}}}})
		}))
		defer stub.Close()
		model := modelapi.New(modelapi.Options{BaseURL: stub.URL + "/v1", Timeout: 3 * time.Second, MaxTokens: 8192, MaxResponseBytes: 1024, Temperature: 0.6, TopP: 0.95, Concurrency: 1})
		defer model.Close()
		processor := service.NewGuard(repo, store, model, policy, "saved-thinking-model", service.Options{
			Lease: time.Minute, Timeout: 3 * time.Second, RetryDelay: time.Millisecond, MaxAttempts: 2,
		})
		if worked, err := processor.ProcessNext(ctx); err != nil || !worked {
			t.Fatalf("process: %v %v", worked, err)
		}
		if !sawRequest.Load() {
			t.Fatal("model was not called")
		}
		data := info(id)
		if data["status"] != "completed" || data["error"] != nil {
			t.Fatal(data)
		}
		result := data["result"].(map[string]any)
		if result["violation"] != false || result["severity"] != float64(0) || result["rule"] != nil || len(result) != 4 {
			t.Fatal(result)
		}
		serialized, _ := json.Marshal(data)
		if bytes.Contains(serialized, []byte("private thinking")) || bytes.Contains(serialized, []byte("policy_text")) || bytes.Contains(serialized, []byte("storage_key")) {
			t.Fatal("private fields leaked")
		}
	})

	t.Run("invalid-model-output-fails", func(t *testing.T) {
		id := uploadID()
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"not JSON"}}]}`)
		}))
		defer stub.Close()
		model := modelapi.New(modelapi.Options{BaseURL: stub.URL + "/v1", Timeout: 3 * time.Second, MaxTokens: 8192, MaxResponseBytes: 1024, Temperature: 0.6, TopP: 0.95, Concurrency: 1})
		defer model.Close()
		processor := service.NewGuard(repo, store, model, policy, "saved-thinking-model", service.Options{
			Lease: time.Minute, Timeout: 3 * time.Second, MaxAttempts: 2,
		})
		for i := 0; i < 2; i++ {
			if worked, err := processor.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("retry %d: %v %v", i, worked, err)
			}
		}
		data := info(id)
		if data["status"] != "failed" || data["result"] != nil || data["error"].(map[string]any)["code"] != "invalid_assessment" {
			t.Fatal(data)
		}
	})

	create := func(t *testing.T) string {
		t.Helper()
		id, err := domain.NewID()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := repo.Create(ctx, domain.ImageRecord{ID: id, StorageKey: id + ".webp", SizeBytes: 1, Width: 1, Height: 1,
			Status: domain.StatusPending, PolicyText: policy.Text(), PolicyHash: policy.Hash(), Model: "thinking", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	expire := func(t *testing.T, id string) {
		t.Helper()
		if err := repo.db.WithContext(ctx).Exec("UPDATE images SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Run("expired-lease-and-fencing", func(t *testing.T) {
		id := create(t)
		first, err := repo.Claim(ctx, time.Minute, 3)
		if err != nil || first == nil {
			t.Fatalf("claim: %v", err)
		}
		expire(t, id)
		second, err := repo.Claim(ctx, time.Minute, 3)
		if err != nil || second == nil {
			t.Fatalf("reclaim: %v", err)
		}
		if first.ClaimToken == second.ClaimToken || second.Image.Attempts != 2 {
			t.Fatal("claim was not renewed")
		}
		assessment := domain.Assessment{Reason: "Safe."}
		if err := repo.Complete(ctx, *first, assessment); !errors.Is(err, domain.ErrLostClaim) {
			t.Fatalf("stale completion: %v", err)
		}
		if err := repo.Complete(ctx, *second, assessment); err != nil {
			t.Fatal(err)
		}
		record, err := repo.Get(ctx, id)
		if err != nil || record.Status != domain.StatusCompleted || record.Result.Violation || record.Result.Severity != 0 {
			t.Fatalf("zero values not saved: %+v %v", record, err)
		}
	})
	t.Run("exhausted-crash", func(t *testing.T) {
		id := create(t)
		job, err := repo.Claim(ctx, time.Minute, 1)
		if err != nil || job == nil {
			t.Fatal(err)
		}
		expire(t, id)
		job, err = repo.Claim(ctx, time.Minute, 1)
		if err != nil || job != nil {
			t.Fatalf("exhausted job reclaimed: %v", err)
		}
		record, err := repo.Get(ctx, id)
		if err != nil || record.Status != domain.StatusFailed || record.Failure == nil {
			t.Fatalf("exhausted job stuck: %+v %v", record, err)
		}
	})
	t.Run("concurrent-claims", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			create(t)
		}
		jobs := make(chan *domain.Job, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				job, err := repo.Claim(ctx, time.Minute, 3)
				if err != nil {
					t.Error(err)
					return
				}
				jobs <- job
			}()
		}
		wg.Wait()
		close(jobs)
		seen := map[string]bool{}
		for job := range jobs {
			if job == nil || seen[job.Image.ID] {
				t.Fatal("duplicate or missing job")
			}
			seen[job.Image.ID] = true
		}
		if len(seen) != 8 {
			t.Fatalf("claimed %d jobs", len(seen))
		}
	})
}
