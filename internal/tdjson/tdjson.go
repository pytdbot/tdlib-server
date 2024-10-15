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
func NewTdJson(create_client bool, verbosity int) *TdJson {
	instance := TdJson{}

	if create_client {
		instance.ClientID = C.td_create_client_id()
		instance.Execute(utils.UnsafeMarshal(
			utils.MakeObject(
				"setLogVerbosityLevel",
				utils.Params{
					"new_verbosity_level": verbosity,
				},
			),
		))
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
