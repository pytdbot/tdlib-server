package server

import (
	"github.com/Masterminds/semver/v3"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

const Version = "0.2.0"
const AppName = "TDLib Server"
const MinimumTDLibVersion = "1.8.6"

var TDLibVersion string

func init() {
	resVersion := utils.UnsafeUnmarshal(tdjson.New(false, 0, "").Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"getOption",
			utils.Params{
				"name": "version",
			},
		),
	)))

	currentVersion, _ := semver.NewVersion(resVersion["value"].(string))

	minimumVersion, _ := semver.NewVersion(MinimumTDLibVersion)

	if currentVersion.LessThan(minimumVersion) {
		utils.PanicOnErr(false, "Current TDLib version is too old. The minimum TDLib version is v%v", MinimumTDLibVersion, true)
	}

	TDLibVersion = currentVersion.Original()
}
