package server

import (
	"github.com/Masterminds/semver/v3"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

const Version = "0.3.0"
const AppName = "TDLib Server"
const MINIMUM_TDLIB_VERSION = "1.8.6"

var TDLIB_VERSION string

func init() {
	resVersion := utils.UnsafeUnmarshal(tdjson.NewTdJson(false, 0, "").Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"getOption",
			utils.Params{
				"name": "version",
			},
		),
	)))
	if resVersion == nil {
		utils.PanicOnErr(false, "Failed to get TDLib version", nil, true)
	}

	currentVersion, _ := semver.NewVersion(resVersion["value"].(string))

	minimumVersion, _ := semver.NewVersion(MINIMUM_TDLIB_VERSION)

	if currentVersion.LessThan(minimumVersion) {
		utils.PanicOnErr(false, "Current TDLib version is too old. The minimum TDLib version is v%v", MINIMUM_TDLIB_VERSION, true)
	}

	TDLIB_VERSION = currentVersion.Original()
}
