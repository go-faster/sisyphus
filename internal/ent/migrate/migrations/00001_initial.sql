-- +goose Up
-- +goose StatementBegin

CREATE TABLE documents (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    source character varying NOT NULL,
    source_id character varying NOT NULL,
    source_url character varying,
    title character varying,
    body text,
    body_hash character varying NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    captured_at timestamp with time zone NOT NULL DEFAULT now(),

    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX documents_source_source_id_body_hash_idx ON documents (source, source_id, body_hash);
CREATE INDEX documents_metadata_idx ON documents USING gin (metadata);

CREATE TABLE chunks (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    document_id uuid NOT NULL,
    chunk_index bigint NOT NULL,
    chunk_type character varying NOT NULL,
    title character varying,
    text text NOT NULL,
    text_hash character varying NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    token_count bigint,
    qdrant_point_id uuid,

    PRIMARY KEY (id),
    FOREIGN KEY (document_id) REFERENCES documents (id)
);

CREATE UNIQUE INDEX chunks_document_id_chunk_index_text_hash_idx ON chunks (document_id, chunk_index, text_hash);
CREATE INDEX chunks_metadata_idx ON chunks USING gin (metadata);

CREATE TABLE support_requests (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    chat_id bigint NOT NULL,
    first_message_id bigint NOT NULL,
    last_message_id bigint,
    source_url character varying,
    raw_text text NOT NULL,
    summary text,
    service_guess character varying,
    severity_guess character varying,
    status character varying NOT NULL DEFAULT 'new',
    confidence double precision,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),

    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX support_requests_chat_id_first_message_id_idx ON support_requests (chat_id, first_message_id);
CREATE INDEX support_requests_status_idx ON support_requests (status);

CREATE TABLE sync_states (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    source character varying NOT NULL,
    last_synced_at timestamp with time zone,
    last_cursor character varying NOT NULL DEFAULT '',
    status character varying NOT NULL DEFAULT 'new',
    error text,
    document_count bigint NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),

    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX sync_states_source_idx ON sync_states (source);
CREATE INDEX sync_states_status_idx ON sync_states (status);

CREATE TABLE telegram_messages (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    thread_id bigint,
    sender_id bigint,
    sender_name character varying,
    text text,
    message_date timestamp with time zone NOT NULL,
    reply_to_id bigint,
    raw_json jsonb NOT NULL DEFAULT '{}',
    created_at timestamp with time zone NOT NULL DEFAULT now(),

    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX telegram_messages_chat_id_message_id_idx ON telegram_messages (chat_id, message_id);
CREATE INDEX telegram_messages_chat_id_message_date_idx ON telegram_messages (chat_id, message_date);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS telegram_messages;
DROP TABLE IF EXISTS sync_states;
DROP TABLE IF EXISTS support_requests;
DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS documents;

-- +goose StatementEnd
