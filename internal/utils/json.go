package utils

import (
	"fmt"

	"github.com/bytedance/sonic"
)

var json = sonic.Config{UseInt64: true}.Froze()

// Marshal encodes the provided map[string]interface{} input into a JSON string.
//
// If an error occurs during encoding, it returns an empty string and the error.
func Marshal(input map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(input)
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

// MarshalWithIndent encodes the provided map[string]interface{} input into a 4-indented JSON string.
//
// If an error occurs during encoding, it returns an empty string and the error.
func MarshalWithIndent(input map[string]interface{}) (string, error) {
	jsonData, err := json.MarshalIndent(input, "", "    ")
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

// Unmarshal decodes the input JSON string into a map[string]interface{}.
//
// If an error occurs during decoding, it returns nil and the error.
func Unmarshal(input string) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		return nil, err
	}

	return result, nil
}

// UnsafeMarshal encodes the provided map[string]interface{} input into a JSON string.
//
// If an error occurs during encoding, it prints the error and returns an empty string.
func UnsafeMarshal(input map[string]interface{}) string {
	jsonData, err := json.Marshal(input)
	if err != nil {
		fmt.Println("error encoding JSON:", err)
		return ""
	}

	return string(jsonData)
}

// UnsafeMarshalWithIndent encodes the provided map[string]interface{} input into an indented JSON string.
//
// If an error occurs during encoding, it prints the error and returns an empty string.
func UnsafeMarshalWithIndent(input map[string]interface{}) string {
	jsonData, err := json.MarshalIndent(input, "", "    ")
	if err != nil {
		fmt.Println("error encoding JSON:", err)
		return ""
	}

	return string(jsonData)
}

// UnsafeUnmarshal decodes the input JSON string into a map[string]interface{}.
//
// If an error occurs during decoding, it prints the error and returns nil.
func UnsafeUnmarshal(input string) map[string]interface{} {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		fmt.Printf("error decoding JSON: %v", err)
		return nil
	}

	return result
}
