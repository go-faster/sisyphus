-- Create "documents" table
CREATE TABLE "documents" ("id" uuid NOT NULL, "source" character varying NOT NULL, "source_id" character varying NOT NULL, "source_url" character varying NULL, "title" character varying NULL, "body" text NULL, "body_hash" character varying NOT NULL, "metadata" jsonb NOT NULL DEFAULT '{}', "created_at" timestamptz NULL, "updated_at" timestamptz NULL, "captured_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "document_metadata" to table: "documents"
CREATE INDEX "document_metadata" ON "documents" USING gin ("metadata");
-- Create index "document_source_source_id_body_hash" to table: "documents"
CREATE UNIQUE INDEX "document_source_source_id_body_hash" ON "documents" ("source", "source_id", "body_hash");
-- Create "support_requests" table
CREATE TABLE "support_requests" ("id" uuid NOT NULL, "chat_id" bigint NOT NULL, "first_message_id" bigint NOT NULL, "last_message_id" bigint NULL, "source_url" character varying NULL, "raw_text" text NOT NULL, "summary" text NULL, "service_guess" character varying NULL, "severity_guess" character varying NULL, "status" character varying NOT NULL DEFAULT 'new', "confidence" double precision NULL, "metadata" jsonb NOT NULL DEFAULT '{}', "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "supportrequest_chat_id_first_message_id" to table: "support_requests"
CREATE UNIQUE INDEX "supportrequest_chat_id_first_message_id" ON "support_requests" ("chat_id", "first_message_id");
-- Create index "supportrequest_status" to table: "support_requests"
CREATE INDEX "supportrequest_status" ON "support_requests" ("status");
-- Create "sync_states" table
CREATE TABLE "sync_states" ("id" uuid NOT NULL, "source" character varying NOT NULL, "last_synced_at" timestamptz NULL, "last_cursor" character varying NOT NULL DEFAULT '', "status" character varying NOT NULL DEFAULT 'new', "error" text NULL, "document_count" bigint NOT NULL DEFAULT 0, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "syncstate_source" to table: "sync_states"
CREATE UNIQUE INDEX "syncstate_source" ON "sync_states" ("source");
-- Create index "syncstate_status" to table: "sync_states"
CREATE INDEX "syncstate_status" ON "sync_states" ("status");
-- Create "telegram_messages" table
CREATE TABLE "telegram_messages" ("id" uuid NOT NULL, "chat_id" bigint NOT NULL, "message_id" bigint NOT NULL, "thread_id" bigint NULL, "sender_id" bigint NULL, "sender_name" character varying NULL, "text" text NULL, "message_date" timestamptz NOT NULL, "reply_to_id" bigint NULL, "raw_json" jsonb NOT NULL DEFAULT '{}', "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "telegrammessage_chat_id_message_date" to table: "telegram_messages"
CREATE INDEX "telegrammessage_chat_id_message_date" ON "telegram_messages" ("chat_id", "message_date");
-- Create index "telegrammessage_chat_id_message_id" to table: "telegram_messages"
CREATE UNIQUE INDEX "telegrammessage_chat_id_message_id" ON "telegram_messages" ("chat_id", "message_id");
-- Create "chunks" table
CREATE TABLE "chunks" ("id" uuid NOT NULL, "chunk_index" bigint NOT NULL, "chunk_type" character varying NOT NULL, "title" character varying NULL, "text" text NOT NULL, "text_hash" character varying NOT NULL, "metadata" jsonb NOT NULL DEFAULT '{}', "token_count" bigint NULL, "qdrant_point_id" uuid NULL, "document_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chunks_documents_chunks" FOREIGN KEY ("document_id") REFERENCES "documents" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "chunk_document_id_chunk_index_text_hash" to table: "chunks"
CREATE UNIQUE INDEX "chunk_document_id_chunk_index_text_hash" ON "chunks" ("document_id", "chunk_index", "text_hash");
-- Create index "chunk_metadata" to table: "chunks"
CREATE INDEX "chunk_metadata" ON "chunks" USING gin ("metadata");
