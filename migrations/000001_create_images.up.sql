BEGIN;

CREATE TABLE images (
    id uuid PRIMARY KEY,
    storage_key text NOT NULL UNIQUE,
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    width integer NOT NULL CHECK (width > 0),
    height integer NOT NULL CHECK (height > 0),
    status text NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    policy_text text NOT NULL,
    policy_hash char(64) NOT NULL,
    model text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    claim_token uuid,
    lease_until timestamptz,
    violation boolean,
    severity integer CHECK (severity BETWEEN 0 AND 10),
    rule_id text,
    rule_name text,
    reason text,
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((status = 'processing') = (claim_token IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK (status = 'processing' OR (claim_token IS NULL AND lease_until IS NULL)),
    CHECK (
        (status = 'completed' AND violation IS NOT NULL AND severity IS NOT NULL
         AND reason IS NOT NULL AND length(btrim(reason)) > 0
         AND error_code IS NULL AND error_message IS NULL
         AND ((violation AND severity > 0 AND rule_id IS NOT NULL AND rule_name IS NOT NULL)
              OR (NOT violation AND severity = 0 AND rule_id IS NULL AND rule_name IS NULL)))
        OR
        (status <> 'completed' AND violation IS NULL AND severity IS NULL
         AND rule_id IS NULL AND rule_name IS NULL AND reason IS NULL)
    ),
    CHECK ((status = 'failed') = (error_code IS NOT NULL AND error_message IS NOT NULL)),
    CHECK (status = 'failed' OR (error_code IS NULL AND error_message IS NULL))
);

CREATE INDEX images_pending_idx ON images (available_at, created_at, id) WHERE status = 'pending';
CREATE INDEX images_processing_idx ON images (lease_until, id) WHERE status = 'processing';

COMMIT;
