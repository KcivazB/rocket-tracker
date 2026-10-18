package store

// SchemaVersion is the newest application schema understood by this binary.
// External sources are inspected read-only; they are never migrated on import.
func SchemaVersion() int { return len(migrations) }
