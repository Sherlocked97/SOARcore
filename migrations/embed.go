// Package migrations holds the SQL migration files for the core schema
// alongside a tiny Go file that exposes them as an embed.FS.
//
// Why this file exists
// --------------------
// Go's //go:embed directive resolves paths relative to the source file's
// directory and cannot escape that directory with "..". Our migration
// SQL lives at repo root in /migrations, so the embed directive has to
// live in the same directory. This file is the smallest possible Go
// package that lets us embed those files and import the FS from cmd/core.
//
// The package has no other code on purpose: SQL changes go in .sql files,
// and this file should never be the reason a contributor opens this
// directory.
package migrations

import "embed"

// FS holds every .sql file in this directory. golang-migrate's iofs
// source then walks it for "<version>_<name>.<up|down>.sql" entries.
//
//go:embed *.sql
var FS embed.FS
