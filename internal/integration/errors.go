package integration

import "fmt"

// ManifestError is a typed manifest-validation failure. Code is a stable
// dotted identifier in the diagnostics idiom (e.g. "manifest.id.invalid"), so
// a drafting harness or improvement loop can match a rejection without parsing
// prose; Detail states the exact rule violated with the violating specifics.
type ManifestError struct {
	Code   string
	Detail string
}

func (e *ManifestError) Error() string { return e.Code + ": " + e.Detail }

// UnsupportedCapabilityError rejects a manifest that declares a capability
// whose type or version this schema does not recognize. It is a distinct type
// rather than a generic manifest error because the contract is explicit: an
// unknown capability is refused, never ignored, and never resolved by invoking
// package code to discover its behavior (ADR 0014).
type UnsupportedCapabilityError struct {
	CapabilityID string
	Type         string
	Version      int
}

func (e *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("manifest.capability.unsupported: capability %q declares type %q version %d, which this schema does not recognize",
		e.CapabilityID, e.Type, e.Version)
}

// StoreError is a typed store-operation failure carrying a stable dotted code
// and a bounded, credential-free detail. Install, verification, and resolution
// failures are legible to an operator and matchable by a later consumer.
type StoreError struct {
	Code   string
	Detail string
}

func (e *StoreError) Error() string { return e.Code + ": " + e.Detail }

func storeErrorf(code, format string, args ...any) *StoreError {
	return &StoreError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func manifestErrorf(code, format string, args ...any) *ManifestError {
	return &ManifestError{Code: code, Detail: fmt.Sprintf(format, args...)}
}
