package utils

import (
	"strconv"
	"strings"
)

// BotIDFromToken extracts and returns the bot ID from the provided token string.
//
// If the token is longer than 80 characters, does not contain ':',
// the ID portion is empty, or the ID is not a valid integer, it returns an empty string.
func BotIDFromToken(token string) string {
	if len(token) > 80 {
		return ""
	}
	idx := strings.Index(token, ":")
	if idx <= 0 {
		return ""
	}
	id := token[:idx]
	if _, err := strconv.Atoi(id); err != nil {
		return ""
	}
	return id
}

// Check if a value can be parsed as an integer
func IsInt(value string) bool {
	_, err := strconv.Atoi(value)
	return err == nil
}

// Check if a value is a boolean
func IsBool(value string) bool {
	_, err := strconv.ParseBool(value)
	return err == nil
}
