package tdjson

// #include <stdlib.h>
// #include <td/telegram/td_json_client.h>
import "C"

import (
	"unsafe"

	"github.com/pytdbot/tdlib-server/internal/utils"
)

type TdJson struct {
	ClientID C.int
}

// New initializes a new TdJson instance and sets the log verbosity level.
func NewTdJson(create_client bool, verbosity int, log_file string) *TdJson {
	instance := TdJson{}

	if create_client {
		if log_file != "" {
			instance.Execute(utils.UnsafeMarshal(
				utils.MakeObject(
					"setLogStream",
					utils.Params{
						"log_stream": utils.MakeObject("logStreamFile", utils.Params{
							"path":            log_file,
							"max_file_size":   104857600, // 100MB
							"redirect_stderr": false,
						}),
					},
				),
			))
		}

		instance.Execute(utils.UnsafeMarshal(
			utils.MakeObject(
				"setLogVerbosityLevel",
				utils.Params{
					"new_verbosity_level": verbosity,
				},
			),
		))

		instance.ClientID = C.td_create_client_id()
	}

	return &instance
}

// Send sends request to the TDLib client.
func (td *TdJson) Send(request string) {
	cstr := C.CString(request)
	C.td_send(td.ClientID, cstr)
	C.free(unsafe.Pointer(cstr))
}

// Receive receives incoming updates and request responses
func (td *TdJson) Receive(timeout float32) string {
	res := C.td_receive(C.double(timeout))

	if res == nil {
		return ""
	}

	return C.GoString(res)
}

// Execute synchronously executes a TDLib request.
func (td *TdJson) Execute(request string) string {
	cstr := C.CString(request)
	res := C.td_execute(cstr)
	C.free(unsafe.Pointer(cstr))

	if res == nil {
		return ""
	}

	return C.GoString(res)
}

// ReceiveBytes receives incoming updates and request responses as a byte slice,
// avoiding the C.GoString→[]byte roundtrip when the result is immediately unmarshalled.
func (td *TdJson) ReceiveBytes(timeout float32) []byte {
	res := C.td_receive(C.double(timeout))

	if res == nil {
		return nil
	}

	return C.GoBytes(unsafe.Pointer(res), C.int(C.strlen(res)))
}
