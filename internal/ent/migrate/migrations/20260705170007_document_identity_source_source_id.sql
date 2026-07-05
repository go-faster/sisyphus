-- Create index "document_source_source_id" to table: "documents"
CREATE UNIQUE INDEX "document_source_source_id" ON "documents" ("source", "source_id");
