package utils

import (
	"strconv"
	"strings"
)

// BotIDFromToken extracts and returns the bot ID from the provided token string.
//
// If the token is longer than 80 characters or does not contain ':', it returns an empty string.
func BotIDFromToken(token string) string {
	if len(token) > 80 || !strings.Contains(token, ":") {
		return ""
	}

	for x, char := range token {
		if char == ':' {
			return token[:x]
		}
	}

	return ""
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
