package server

import (
	"github.com/Masterminds/semver/v3"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

const Version = "0.1.12"
const AppName = "TDLib Server"
const MINIMUM_TDLIB_VERSION = "1.8.6"

var TDLIB_VERSION string

func init() {
	res_version := utils.UnsafeUnmarshal(tdjson.NewTdJson(false, 0, "").Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"getOption",
			utils.Params{
				"name": "version",
			},
		),
	)))

	currant_version, _ := semver.NewVersion(res_version["value"].(string))

	minimum_version, _ := semver.NewVersion(MINIMUM_TDLIB_VERSION)

	if currant_version.LessThan(minimum_version) {
		utils.PanicOnErr(false, "Current TDLib version is too old. The minimum TDLib version is v%v", MINIMUM_TDLIB_VERSION, true)
	}

	TDLIB_VERSION = currant_version.Original()
}
