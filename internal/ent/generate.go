// Package ent provides database entity and ORM abstractions.
package ent

//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate ./schema --feature sql/upsert,sql/versioned-migration
