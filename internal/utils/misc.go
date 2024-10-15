package utils

import "fmt"

// Dump pretty print TDLib object
func Dump(input map[string]interface{}) {
	fmt.Println(MarshalWithIndent(input))
}
