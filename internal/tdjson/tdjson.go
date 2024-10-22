package tdjson

// #include <td/telegram/td_json_client.h>
import "C"

import (
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
	C.td_send(td.ClientID, C.CString(request))
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
	res := C.td_execute(C.CString(request))

	if res == nil {
		return ""
	}

	return C.GoString(res)
}
