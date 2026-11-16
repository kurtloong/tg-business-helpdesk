CREATE TABLE worker_states (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 heartbeat_at TIMESTAMPTZ
);
INSERT INTO worker_states (id) VALUES (1);
CREATE INDEX jobs_active_status ON jobs(status,created_at) WHERE status IN ('pending','dead');
