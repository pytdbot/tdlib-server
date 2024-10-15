package utils

// AsMap attempts to convert the provided input to a map[string]interface{}.
//
// If the conversion is successful, it returns the map; otherwise, it returns nil.
func AsMap(input interface{}) map[string]interface{} {
	if m, ok := input.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// KeyExists checks whether the specified key exists in the given map[string]interface{}.
//
// It returns true if the key exists, otherwise false.
func KeyExists(m map[string]interface{}, key string) bool {
	_, exists := m[key]
	return exists
}
