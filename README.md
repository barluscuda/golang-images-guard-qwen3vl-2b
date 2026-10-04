# Images Guard

A Go API that accepts a WebP image, returns its ID, and assesses it asynchronously with a local **Qwen3-VL-2B-Thinking** model through the **llama.cpp server HTTP API**. No model API key is needed. Application settings use YAML; model inference settings are fixed.

Uses Gin, Zap, Viper, PostgreSQL, GORM, local filesystem storage, and Docker. Schema changes are explicit SQL migrations; the application never invokes GORM AutoMigrate.

## Run with Docker Compose

1. Copy `.env.example` to `.env`. Set the database password, matching DSN, and local model endpoint.
2. Start PostgreSQL and apply migrations explicitly:

   ```sh
   docker compose up -d postgres
   docker compose --profile tools run --rm migrate
   ```

3. Start your local model server, then build and start the API and worker:

   ```sh
   docker compose up -d --build api worker
   ```

The API listens at `http://127.0.0.1:8080`. Compose stores PostgreSQL and image files in named volumes. Image files are under `/data/images`; mounts and file names are never exposed by the API. The API and worker run as separate processes and share PostgreSQL and the image volume. Multiple instances must share the same image volume and PostgreSQL database.

The model server is configured separately because GPU/CPU and inference runtime requirements vary. Compose reaches a host server through `host.docker.internal`; that server must listen on an interface reachable from the Docker bridge.

## llama.cpp server

Run a recent `llama-server` with the Qwen3-VL-2B-Thinking GGUF and its matching multimodal projector. For example, using files you have already downloaded:

```sh
llama-server \
  --model /path/to/Qwen3-VL-2B-Thinking.gguf \
  --mmproj /path/to/mmproj.gguf \
  --host 0.0.0.0 --port 8000 \
  --ctx-size 32768 --parallel 1 --jinja \
  --alias Qwen/Qwen3-VL-2B-Thinking
```

The application only calls this server's `/v1/chat/completions` API; it does not load model files or launch an inference runtime. The default API base URL is `http://127.0.0.1:8000/v1`; Compose uses `http://host.docker.internal:8000/v1`. Set `GUARD_MODEL_BASE_URL` to change the address. Run the server without API-key authentication; requests contain no authorization header or dummy key.

Requests enable thinking, separate it with `reasoning_format=deepseek`, and constrain the final answer with a JSON schema. Sampling is fixed at temperature 0.6, top-p 0.95, top-k 20, and min-p 0. The server's `reasoning_content` is ignored; only `choices[0].message.content` is assessed. WebP uploads are converted losslessly to PNG for the server's image decoder, preserving dimensions and decoded pixels. Storage retains the original WebP.

Go rejects mixed reasoning/JSON, missing fields, duplicate keys, extra fields, refusals, and token-truncated outputs. There is no automatic JSON repair or backend fallback. Generation is limited to 8192 tokens including thinking, with a 120-second processing timeout and a 150-second worker lease.

Reference: [llama.cpp server API and multimodal support](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).

## API

Upload exactly one multipart file field named `image`:

```sh
curl -i http://127.0.0.1:8080/v1/images -F 'image=@example.webp'
```

Response: **202 Accepted**, with a `Location` header:

```json
{"image_id":"c8915c6e-d613-4943-9089-7ec4331d42df"}
```

Poll image info:

```sh
curl http://127.0.0.1:8080/v1/images/c8915c6e-d613-4943-9089-7ec4331d42df
```

Delete a completed image and its stored file:

```sh
curl -i -X DELETE http://127.0.0.1:8080/v1/images/c8915c6e-d613-4943-9089-7ec4331d42df
```

Deletion returns **204 No Content** for completed images, **409 Conflict** while the image is pending, processing, or failed, and **404 Not Found** for an unknown ID. GET and DELETE validate lowercase UUIDs.

Example completed response:

```json
{
  "image_id": "c8915c6e-d613-4943-9089-7ec4331d42df",
  "status": "completed",
  "size_bytes": 12345,
  "width": 1920,
  "height": 1080,
  "result": {
    "violation": true,
    "severity": 8,
    "rule": {"id": "SEXUAL_CONTENT", "name": "Sexual Content"},
    "reason": "The image contains explicit sexual content."
  },
  "error": null,
  "created_at": "2026-10-04T00:00:00Z",
  "updated_at": "2026-10-04T00:00:05Z"
}
```

| Status | Result | Error |
| --- | --- | --- |
| `pending` | `null` | `null` |
| `processing` | `null` | `null` |
| `completed` | Assessment object | `null` |
| `failed` | `null` | `{ "code": "...", "message": "..." }` |

A violation is a completed assessment. A failed assessment is never represented as safe. Non-violating results use `violation=false`, `severity=0`, `rule=null`, and a nonempty reason. Violations require severity 1–10 and an exact ID/name pair from the saved policy. When multiple rules match, the model selects the most severe rule. Severity is an ordinal score, not a calibrated probability.

Errors use `{"error":{"code":"...","message":"..."}}`. Invalid uploads return 400, byte limits 413, non-WebP 415, excessive dimensions 422, unknown IDs 404, attempts to delete an unprocessed image 409, and unavailable upload/database capacity 503. Polling responses include `Cache-Control: no-store`; every routed request has `X-Request-ID`.

Health endpoints: `GET /health/live` and `GET /health/ready`. Readiness checks PostgreSQL; model availability is reflected in job outcomes.

## Image limits

- One static WebP; lossless, lossy, and alpha are supported.
- Maximum file size: **5 MiB** by default.
- Long side ≤1920 and short side ≤1080, including portrait orientation; configurable limits can be reduced.
- Whole-request cap: file limit +64 KiB for multipart framing.
- Actual RIFF/WebP contents, container lengths, animation markers, dimensions, and full decoding are checked. Filename and claimed MIME type do not establish the format.
- Rejected uploads do not create an image record. Valid images are assessed without resizing; the inference adapter converts WebP to PNG.

## Policy and configuration

Edit `config/policy.txt` using natural language, with each rule introduced by:

```text
RULE SEXUAL_CONTENT | Sexual Content
Describe the content this rule prohibits and any exceptions.
```

IDs must be uppercase letters, digits, and underscores, starting with a letter. Declare 1–64 unique rules; policy size is limited to 64 KiB. The supplied file contains a sample sexual-content rule; edit it to match your intended policy.

Restart after changing policy. Each accepted upload saves the policy text/hash and model name, so retries and crash recovery retain that job's policy. Existing jobs do not adopt a new policy.

`config/config.yaml` configures the server, upload limits, storage, database, workers, policy path, and logging. Environment variables override these settings using `GUARD_` plus the uppercase dotted key with underscores: for example, `upload.max_bytes` → `GUARD_UPLOAD_MAX_BYTES`. `GUARD_DATABASE_DSN` is required unless `database.dsn` is set in YAML. Use `-config /path/to/config.yaml` to select another file; paths inside it are relative to the process working directory. Unknown keys and invalid limits fail startup.

There is no `model` section in YAML. The application only calls the llama.cpp API, with fixed model name, response format, timeout, token limit, and sampling settings. `GUARD_MODEL_BASE_URL` selects the API address and defaults to `http://127.0.0.1:8000/v1`. No API key is used. The server loads Qwen3-VL-2B-Thinking; use the alias shown above.

## Architecture and recovery

```text
cmd/api ──► adapter/http ──► service ──► domain
                                  ├──► port.ImageRepository ◄── repository
                                  └──► port.ImageStorage ◄───── adapter/storage

cmd/worker ──► adapter/worker ──► service ──► port.Model ◄── adapter/llamacpp
                                      │
                                      └──► repository ──► GORM ──► adapter/postgres

cmd/migrate ──► migrations/*.sql ──► PostgreSQL
```

The domain has no infrastructure dependencies. `cmd/api` owns the HTTP server, `cmd/worker` owns inference and orphan reconciliation, and `cmd/migrate` applies SQL migrations. GORM records and API/model JSON formats stay outside the domain.

PostgreSQL is the durable work queue. Workers use `FOR UPDATE SKIP LOCKED` in a short transaction, then release the transaction before inference. Claim tokens fence result writes, and expired leases allow recovery after crashes. Failures retry up to three attempts with increasing delays; exhausted jobs become `failed`. Database completion failures leave the lease available for recovery.

Files are written through a temporary file, synced, and atomically renamed before creating the pending row. If database creation fails, file deletion occurs only after confirming no row exists. Startup and hourly reconciliation remove unreferenced files older than one hour. Completed images can be removed through the DELETE endpoint; this version has no image-download endpoint.

SQL migrations under `migrations/` are applied by `cmd/migrate`. Startup requires clean schema version 1 and performs no schema changes.

## Build and checks

Requires Go 1.25 or newer.

```sh
go build -o bin/api ./cmd/api
go build -o bin/worker ./cmd/worker
go build -o bin/migrate ./cmd/migrate
go test ./...
go vet ./...
```

For a local run against an already migrated PostgreSQL database:

```sh
GUARD_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' ./bin/api
GUARD_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' ./bin/worker
```

Apply migrations locally with `GUARD_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' ./bin/migrate up`.

PostgreSQL integration checks are opt-in:

```sh
GUARD_TEST_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' go test ./internal/repository -v
```

Integration checks create and remove their own uniquely named schema. They cover migration reversibility, asynchronous upload/status, invalid model output, concurrent claims, expired leases, and stale result fencing. Model requests in checks use an HTTP stub; assessing real model quality and runtime compatibility requires your running llama.cpp server.
