-- Create "queue_jobs" table
CREATE TABLE "queue_jobs" ("id" uuid NOT NULL, "queue" character varying NOT NULL, "dedup_key" character varying NOT NULL, "payload" bytea NULL, "status" character varying NOT NULL DEFAULT 'pending', "attempts" bigint NOT NULL DEFAULT 0, "max_attempts" bigint NOT NULL DEFAULT 5, "visible_at" timestamptz NOT NULL, "lease_owner" character varying NULL, "error" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "completed_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "queuejob_queue_dedup_key" to table: "queue_jobs"
CREATE UNIQUE INDEX "queuejob_queue_dedup_key" ON "queue_jobs" ("queue", "dedup_key");
-- Create index "queuejob_queue_visible_at" to table: "queue_jobs"
CREATE INDEX "queuejob_queue_visible_at" ON "queue_jobs" ("queue", "visible_at") WHERE status IN ('pending', 'running');
