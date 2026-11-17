-- Historical routes and completed jobs remain available for audit.
-- Never treat an A-account contact as a user who has started this bot.
CREATE TABLE bot_routes (
 id BIGSERIAL PRIMARY KEY,
 customer_id BIGINT NOT NULL UNIQUE,
 group_id BIGINT NOT NULL,
 thread_id BIGINT NOT NULL DEFAULT 0,
 last_incoming TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX bot_routes_topic ON bot_routes(group_id,thread_id) WHERE thread_id>0;
-- Old queued replies must not be reinterpreted as direct-bot conversations.
UPDATE jobs SET status='dead', last_error='Business mode retired: review historical queue manually', updated_at=now()
 WHERE status='pending';
