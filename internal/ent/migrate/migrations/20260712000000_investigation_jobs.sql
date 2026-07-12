-- Create "investigation_jobs" table
CREATE TABLE "investigation_jobs" ("id" uuid NOT NULL, "idempotency_key" character varying NOT NULL, "description" text NOT NULL, "status" character varying NOT NULL DEFAULT 'pending', "report" jsonb NULL, "iterations" bigint NOT NULL DEFAULT 0, "tools_used" bigint NOT NULL DEFAULT 0, "error_message" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "started_at" timestamptz NULL, "completed_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "investigationjob_idempotency_key" to table: "investigation_jobs"
CREATE UNIQUE INDEX "investigationjob_idempotency_key" ON "investigation_jobs" ("idempotency_key");
-- Create index "investigationjob_status" to table: "investigation_jobs"
CREATE INDEX "investigationjob_status" ON "investigation_jobs" ("status");
