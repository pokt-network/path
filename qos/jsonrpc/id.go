package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// ID represents a JSON-RPC request/response identifier.
//
// JSON-RPC 2.0 specification requirements:
//   - Must be String, Number, or NULL
//   - Server must echo the same value in Response
//   - Should not be NULL in normal operation
//   - Numbers must not contain fractional parts
//
// Reference: https://www.jsonrpc.org/specification#id
type ID struct {
	intID *int
	strID *string
}

// String returns the ID as a string representation.
// Priority order: integers first, then strings, then "null" for unset IDs.
func (id ID) String() string {
	if id.intID != nil {
		return fmt.Sprintf("%d", *id.intID)
	}
	if id.strID != nil {
		return *id.strID
	}
	return "null"
}

// IsEmpty returns true when the ID is unset (both pointers are nil).
func (id ID) IsEmpty() bool {
	return id.intID == nil && id.strID == nil
}

// MarshalJSON implements json.Marshaler interface.
// Priority order: integers as JSON numbers, strings as JSON strings, unset as null.
func (id ID) MarshalJSON() ([]byte, error) {
	if id.intID != nil {
		return json.Marshal(*id.intID)
	}
	if id.strID != nil {
		return json.Marshal(*id.strID)
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements json.Unmarshaler interface.
// Handles JSON-RPC ID values according to specification:
//   - null or "" → unset ID (both pointers nil)
//   - integers → stored in intID
//   - strings → stored in strID
func (id *ID) UnmarshalJSON(data []byte) error {
	// Check for null or empty string first
	if string(data) == "null" || string(data) == `""` {
		id.intID = nil
		id.strID = nil
		return nil
	}

	// Try to unmarshal as int
	var intID int
	if err := json.Unmarshal(data, &intID); err == nil {
		id.intID = &intID
		id.strID = nil
		return nil
	}

	// Try to unmarshal as string
	var strID string
	err := json.Unmarshal(data, &strID)
	if err != nil {
		return err
	}

	id.strID = &strID
	id.intID = nil
	return nil
}

// IDFromInt creates an ID from an integer value.
func IDFromInt(id int) ID {
	return ID{intID: &id}
}

// IDFromStr creates an ID from a string value.
func IDFromStr(id string) ID {
	return ID{strID: &id}
}

// IDFromString parses a string representation of an ID.
// If the string can be parsed as an integer, creates an integer ID.
// Otherwise, creates a string ID.
// Empty strings return an empty ID (null).
func IDFromString(idStr string) ID {
	if idStr == "" || idStr == "null" {
		return ID{}
	}

	// Try to parse as integer first (most common case for JSON-RPC)
	var intVal int
	if _, err := fmt.Sscanf(idStr, "%d", &intVal); err == nil {
		return IDFromInt(intVal)
	}

	// Fall back to string ID
	return IDFromStr(idStr)
}

// Equal returns true if two IDs represent the same value.
// Compares the underlying values, not pointer addresses.
// Handles cross-type comparisons (int vs string) for JSON-RPC compatibility,
// as some endpoints return string IDs even when given integer IDs.
func (id ID) Equal(other ID) bool {
	// Both are unset
	if id.IsEmpty() && other.IsEmpty() {
		return true
	}

	// One is unset, the other isn't
	if id.IsEmpty() != other.IsEmpty() {
		return false
	}

	// Both have int values
	if id.intID != nil && other.intID != nil {
		return *id.intID == *other.intID
	}

	// Both have string values
	if id.strID != nil && other.strID != nil {
		return *id.strID == *other.strID
	}

	// Cross-type comparison: int vs string
	// Some endpoints return string IDs even when given integer IDs.
	// Per JSON-RPC spec, the server should echo back the same value,
	// but type preservation is not strictly required by all implementations.
	if id.intID != nil && other.strID != nil {
		// Compare id (int) with other (string)
		return id.String() == *other.strID
	}
	if id.strID != nil && other.intID != nil {
		// Compare id (string) with other (int)
		return *id.strID == other.String()
	}

	// Should not reach here, but return false as fallback
	return false
}
