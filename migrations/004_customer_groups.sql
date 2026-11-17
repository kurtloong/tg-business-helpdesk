-- Existing private routes retain their IDs and staff Topics.
ALTER TABLE bot_routes ADD COLUMN customer_thread_id BIGINT NOT NULL DEFAULT 0 CHECK (customer_thread_id >= 0);
ALTER TABLE bot_routes DROP CONSTRAINT bot_routes_customer_id_key;
CREATE UNIQUE INDEX bot_routes_customer_thread ON bot_routes(customer_id, customer_thread_id);
