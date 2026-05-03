// Package schemaregistry holds the canonical entity schemas and provides a
// single place where the rest of the system asks "is this attributes blob
// valid for entity X at version V?".
//
// Why this exists as its own package
// ----------------------------------
// The Incident table has a JSONB `attributes` column that carries extension
// fields. Without a registry, every handler would invent its own validation,
// which is exactly the schema-evolution failure mode this design avoids. The
// registry centralizes:
//
//   - The set of known entity types and their versions.
//   - A compiled JSON Schema validator per (type, version).
//   - A single Validate() entry point used by the domain layer.
//
// Schemas are embedded into the binary at build time via go:embed, so the
// running service never has to read files from disk.
package schemaregistry

import (
	"bytes"         // wraps a []byte as an io.Reader, which jsonschema needs.
	"embed"         // standard-library helper for embedding files into the binary.
	"encoding/json" // for unmarshalling the embedded schema bytes into a generic value.
	"errors"
	"fmt"

	// santhosh-tekuri/jsonschema/v6 is a pure-Go, draft-2020-12 JSON Schema
	// validator. We picked it over alternatives because it has no cgo
	// dependency, supports the latest draft, and is actively maintained.
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemasFS embeds the contents of the schemas/ directory into the compiled
// binary. The //go:embed directive is a Go feature that, at build time,
// reads the listed files and exposes them as an embed.FS. This means the
// service does not need access to the source tree at runtime.
//
//go:embed schemas/*.json
var schemasFS embed.FS

// EntityType is a typed alias for an entity name (e.g. "incident"). Using a
// distinct type instead of a bare string makes signatures self-documenting
// and lets the Go compiler catch accidental mixups with arbitrary strings.
type EntityType string

// Common entity types live here so callers can refer to them by name and
// the compiler can flag typos. Add new entities by adding a new constant
// and a corresponding JSON Schema file in schemas/.
const (
	EntityIncident EntityType = "incident"
)

// ErrSchemaNotFound is returned when a caller asks for a (type, version)
// pair that the registry does not know about. It is exported so callers can
// distinguish "I asked for the wrong thing" from "validation failed".
var ErrSchemaNotFound = errors.New("schema not found")

// Registry holds compiled validators for every (entity, version) we ship.
// It is safe for concurrent use after construction — the underlying
// validator instances are read-only.
type Registry struct {
	// validators is a map from a composite key (entity + version) to a
	// pre-compiled JSON Schema. Compilation is expensive; we do it once
	// at startup in New().
	validators map[string]*jsonschema.Schema
}

// New builds the registry from the embedded schema files. It is intended to
// be called once during service startup and the resulting *Registry passed
// around to consumers.
//
// If a schema file fails to load or compile, New returns a non-nil error
// and a nil registry — start-up should fail loudly rather than half-load.
func New() (*Registry, error) {
	r := &Registry{validators: make(map[string]*jsonschema.Schema)}

	// In v1 we hard-code the schemas to load. As we add entities, this
	// list grows; alternatively, this can be driven by a directory walk
	// over schemasFS. The hard-coded form is simpler to read.
	type entry struct {
		entity   EntityType
		version  int
		filename string
	}
	entries := []entry{
		{EntityIncident, 1, "schemas/incident_v1.json"},
	}

	for _, e := range entries {
		if err := r.load(e.entity, e.version, e.filename); err != nil {
			// fmt.Errorf with %w wraps the underlying error so callers can
			// inspect it via errors.Is / errors.As if they care.
			return nil, fmt.Errorf("schemaregistry: load %s v%d: %w", e.entity, e.version, err)
		}
	}
	return r, nil
}

// load reads and compiles a single schema file from the embedded FS.
func (r *Registry) load(entity EntityType, version int, filename string) error {
	raw, err := schemasFS.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", filename, err)
	}

	// jsonschema.UnmarshalJSON parses raw JSON into the in-memory shape
	// the compiler expects. We then add it to a fresh compiler and call
	// Compile to get a validator. bytes.NewReader wraps the []byte so it
	// satisfies the io.Reader interface jsonschema asks for.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse %s: %w", filename, err)
	}

	c := jsonschema.NewCompiler()
	// AddResource registers the parsed schema under a URI. We re-use the
	// $id from the schema itself so cross-references work later.
	if err := c.AddResource(filename, doc); err != nil {
		return fmt.Errorf("add resource %s: %w", filename, err)
	}
	compiled, err := c.Compile(filename)
	if err != nil {
		return fmt.Errorf("compile %s: %w", filename, err)
	}
	r.validators[key(entity, version)] = compiled
	return nil
}

// Validate checks that `payload` conforms to the schema registered for
// (entity, version). If the registry doesn't know about that pair, it
// returns ErrSchemaNotFound. If the schema is found but the payload is
// invalid, it returns a non-nil error describing the violation.
//
// payload may be nil (e.g. for entities with no attributes) — in that case
// the schema receives an empty object, which v1's permissive Incident
// schema accepts.
func (r *Registry) Validate(entity EntityType, version int, payload []byte) error {
	v, ok := r.validators[key(entity, version)]
	if !ok {
		return fmt.Errorf("%w: %s v%d", ErrSchemaNotFound, entity, version)
	}
	// jsonschema validates against an arbitrary Go value, so we round-trip
	// the bytes through encoding/json. nil payload becomes {}.
	var doc any
	if len(payload) == 0 {
		doc = map[string]any{}
	} else {
		if err := json.Unmarshal(payload, &doc); err != nil {
			return fmt.Errorf("payload is not valid JSON: %w", err)
		}
	}
	if err := v.Validate(doc); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}

// Has reports whether a (entity, version) pair is registered. Useful for
// callers (e.g. the connector runtime) that want to verify a connector's
// declared event_schema_versions are all known to this core.
func (r *Registry) Has(entity EntityType, version int) bool {
	_, ok := r.validators[key(entity, version)]
	return ok
}

// key builds the map key used internally. Centralizing it avoids subtle
// "did I use ":" or "/" as the separator?" bugs.
func key(entity EntityType, version int) string {
	return fmt.Sprintf("%s:v%d", entity, version)
}
