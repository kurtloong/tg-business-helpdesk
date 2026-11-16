CREATE TABLE IF NOT EXISTS jobs (
 update_id BIGINT PRIMARY KEY,
 source_key TEXT NOT NULL UNIQUE,
 payload JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','done','ignored','dead')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_error TEXT NOT NULL DEFAULT '',
 result_message_id BIGINT NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS jobs_pending ON jobs(next_at,update_id) WHERE status='pending';
CREATE TABLE IF NOT EXISTS routes (
 id BIGSERIAL PRIMARY KEY,
 business_id TEXT NOT NULL,
 customer_id BIGINT NOT NULL,
 group_id BIGINT NOT NULL,
 thread_id BIGINT NOT NULL DEFAULT 0,
 last_incoming TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(business_id,customer_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS routes_topic ON routes(group_id,thread_id) WHERE thread_id>0;
