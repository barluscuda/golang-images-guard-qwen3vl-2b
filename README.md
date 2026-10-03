# Images Guard

A Go API that accepts a WebP image, returns its ID, and assesses it asynchronously with a local **Qwen3-VL-2B-Thinking** model through an OpenAI-compatible endpoint.

Uses Gin, Zap, Viper, PostgreSQL, GORM, local filesystem storage, and Docker. Schema changes are explicit SQL migrations; the application never invokes GORM AutoMigrate.

## Run with Docker Compose

1. Copy `.env.example` to `.env`. Set the database password, matching DSN, and local model endpoint.
2. Start PostgreSQL and apply migrations explicitly:

   ```sh
   docker compose up -d postgres
   docker compose --profile tools run --rm migrate
   ```

3. Start your local model server, then build and start the API:

   ```sh
   docker compose up -d --build guard
   ```

The API listens at `http://127.0.0.1:8080`. Compose stores PostgreSQL and image files in named volumes. Image files are under `/data/images`; mounts and file names are never exposed by the API. API and worker run in the same process. Multiple instances must share the same image volume and PostgreSQL database.

The model server is configured separately because GPU/CPU and inference runtime requirements vary. Compose reaches a host server through `host.docker.internal`; that server must listen on an interface reachable from the Docker bridge.

## Local model

For vLLM on compatible hardware, an example launch configuration is:

```sh
vllm serve Qwen/Qwen3-VL-2B-Thinking \
  --host 0.0.0.0 --port 8000 \
  --reasoning-parser qwen3 \
  --max-model-len 32768 \
  --max-num-seqs 1 \
  --limit-mm-per-prompt '{"image": 1, "video": 0}'
```

Use a vLLM release supporting this checkpoint and reasoning parser. Reasoning must be emitted in a separate server field, with the final JSON in `choices[0].message.content`. Structured constraints must apply to the final answer while preserving thinking. The client requests `chat_template_kwargs.enable_thinking=true` and Qwen's suggested thinking sampling settings (temperature 0.6, top-p 0.95, top-k 20).

For Ollama, set `GUARD_MODEL_BACKEND=ollama`, `GUARD_MODEL_BASE_URL=http://host.docker.internal:11434/v1`, and `GUARD_MODEL_NAME` to your installed **thinking** tag. The adapter requests `reasoning_effort=high`. Confirm the selected tag/server supports thinking, vision, and the configured response format together.

`json_schema` is the default response format. If your server supports only JSON mode, explicitly set `GUARD_MODEL_RESPONSE_FORMAT=json_object`. Go validates the same contract in both modes. There is no automatic format fallback, reasoning removal, JSON repair, or fallback to a hosted OpenAI model. Mixed reasoning/JSON, missing fields, duplicate keys, extra fields, refusals, and token-truncated outputs are rejected.

The 8192-token generation limit includes thinking. Adjust `model.max_tokens`, `model.timeout`, `worker.lease_duration`, and the server context size for your hardware and policy length. The lease must exceed the processing timeout by at least 15 seconds.

References: [Qwen deployment](https://github.com/QwenLM/Qwen3-VL#deployment), [vLLM reasoning](https://docs.vllm.ai/en/latest/features/reasoning_outputs/), [vLLM structured outputs](https://docs.vllm.ai/en/latest/features/structured_outputs/), [Ollama compatibility](https://docs.ollama.com/api/openai-compatibility).

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

Errors use `{"error":{"code":"...","message":"..."}}`. Invalid uploads return 400, byte limits 413, non-WebP 415, excessive dimensions 422, unknown IDs 404, and unavailable upload/database capacity 503. Polling responses include `Cache-Control: no-store`; every routed request has `X-Request-ID`.

Health endpoints: `GET /health/live` and `GET /health/ready`. Readiness checks PostgreSQL; model availability is reflected in job outcomes.

## Image limits

- One static WebP; lossless, lossy, and alpha are supported.
- Maximum file size: **5 MiB** by default.
- Long side ≤1920 and short side ≤1080, including portrait orientation; configurable limits can be reduced.
- Whole-request cap: file limit +64 KiB for multipart framing.
- Actual RIFF/WebP contents, container lengths, animation markers, dimensions, and full decoding are checked. Filename and claimed MIME type do not establish the format.
- Rejected uploads do not create an image record. Valid images are assessed without resizing.

## Policy and configuration

Edit `config/policy.txt` using natural language, with each rule introduced by:

```text
RULE SEXUAL_CONTENT | Sexual Content
Describe the content this rule prohibits and any exceptions.
```

IDs must be uppercase letters, digits, and underscores, starting with a letter. Declare 1–64 unique rules; policy size is limited to 64 KiB. The supplied file contains a sample sexual-content rule; edit it to match your intended policy.

Restart after changing policy. Each accepted upload saves the policy text/hash and model name, so retries and crash recovery retain that job's policy. Existing jobs do not adopt a new policy.

`config/config.yaml` supplies defaults. Environment variables override settings using `GUARD_` plus the uppercase dotted key with underscores: for example, `model.base_url` → `GUARD_MODEL_BASE_URL`. `GUARD_DATABASE_DSN` is required. `-config /path/to/config.yaml` selects another file; file paths inside it are relative to the process working directory. Unknown configuration keys and invalid limits fail startup.

## Architecture and recovery

```text
HTTP adapter ──► core service ──► repository and storage ports
Worker adapter ► core processor ► repository, storage and model ports
                             ◄── PostgreSQL/GORM, filesystem, OpenAI adapters
```

The core has no framework dependencies. `cmd/guard` owns construction and shutdown. GORM records and API/model JSON formats belong to adapters.

PostgreSQL is the durable work queue. Workers use `FOR UPDATE SKIP LOCKED` in a short transaction, then release the transaction before inference. Claim tokens fence result writes, and expired leases allow recovery after crashes. Failures retry up to three attempts with increasing delays; exhausted jobs become `failed`. Database completion failures leave the lease available for recovery.

Files are written through a temporary file, synced, and atomically renamed before creating the pending row. If database creation fails, file deletion occurs only after confirming no row exists. Startup and hourly reconciliation remove unreferenced files older than one hour. Referenced images/results are retained; this version has no expiry or image-download endpoint.

SQL migrations under `migrations/` are applied through the separate migrate command. Startup requires clean schema version 1 and performs no schema changes. [Migration tooling](https://github.com/golang-migrate/migrate).

## Build and checks

Requires Go 1.25 or newer.

```sh
go build -o bin/guard ./cmd/guard
go test ./...
go vet ./...
```

For a local run against an already migrated PostgreSQL database:

```sh
GUARD_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' ./bin/guard
```

PostgreSQL integration checks are opt-in:

```sh
GUARD_TEST_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' go test ./internal/adapters/postgres -v
```

Integration checks create and remove their own uniquely named schema. They cover migration reversibility, asynchronous upload/status, invalid model output, concurrent claims, expired leases, and stale result fencing. Model requests in checks use an HTTP stub; assessing real model quality and runtime compatibility requires your running Qwen endpoint.
