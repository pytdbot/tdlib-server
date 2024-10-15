package utils

// Params represents a map of string keys to interface{} values, used to store
// various parameters in requests and responses.
type Params map[string]interface{}

// MakeObject creates a map with the given requestType and additional parameters.
func MakeObject(requestType string, params Params) map[string]interface{} {
	params["@type"] = requestType
	return params
}

// Type returns the "@type" field from the provided TDLib object as a string.
//
// If the "@type" field is missing, it returns an empty string.
func Type(update map[string]interface{}) string {
	if t, ok := update["@type"]; ok {
		return t.(string)
	}
	return ""
}

// MakeError creates a map representing TDLib error with the given code and message.
func MakeError(code int, message string) map[string]interface{} {
	return MakeObject("error", Params{"code": code, "message": message})
}
