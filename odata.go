package main

import (
	"encoding/json"
)

// Define a custom types for nullable Edm types
type NullableString string
type NullableInt int

// Define MarshalJSON functions to implement nullable JSON encoding
func (ns NullableString) MarshalJSON() ([]byte, error) {
	if ns == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(ns))
}

// Define MarshalJSON functions to implement nullable JSON encoding
func (ni NullableInt) MarshalJSON() ([]byte, error) {
	if ni == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(int(ni))
}
