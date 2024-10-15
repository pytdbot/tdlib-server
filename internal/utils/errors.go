package utils

import (
	"fmt"
	"os"
)

// PanicOnErr checks the error or boolean value and either panics or exits based on the exit flag.
func PanicOnErr(err interface{}, msg string, v interface{}, exit bool) {
	switch e := err.(type) {
	case error:
		if e != nil {
			if exit {
				fmt.Fprintf(os.Stderr, msg+"\n", v)
				os.Exit(1)
			} else {
				panic(fmt.Errorf(msg, v))
			}
		}
	case bool:
		if !e {
			if exit {
				fmt.Fprintf(os.Stderr, msg+"\n", v)
				os.Exit(1)
			} else {
				panic(fmt.Sprintf(msg, v))
			}
		}
	}
}
