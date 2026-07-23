// Package ent provides database entity and ORM abstractions.
package ent

//go:generate go tool ent generate ./schema --feature sql/execquery,sql/upsert,sql/versioned-migration
