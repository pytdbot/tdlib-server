package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	srv "github.com/pytdbot/tdlib-server/internal/server"
	"github.com/pytdbot/tdlib-server/internal/utils"

	"github.com/cheggaaa/pb/v3"
	"github.com/urfave/cli/v2"
)

func main() {
	cli.AppHelpTemplate = `TDLib Server v{{.Version}} - A high-performance Go server built to scale Telegram bots using TDLib and RabbitMQ

COMMANDS:
{{range .VisibleCommands}}{{printf "   %-15s %s\n" (join .Names ", ") .Usage}}{{end}}

OPTIONS:{{template "visibleFlagCategoryTemplate" .}}

{{template "copyrightTemplate" .}}
`

	cli.VersionPrinter = func(cCtx *cli.Context) {
		fmt.Printf("TDLib Server v%s (TDLib v%s)\n", cCtx.App.Version, srv.TDLIB_VERSION)
	}

	app := createCLIApp()
	err := app.Run(os.Args)
	if err != nil {
		utils.PanicOnErr(err, "Could not start server: %v", err, true)
	}
}

func createCLIApp() *cli.App {
	return &cli.App{
		Name:  "tdlib-server",
		Usage: "A high-performance TDLib Server meant for scaling bots",
		Authors: []*cli.Author{
			{Name: "AYMEN ~ https://github.com/AYMENJD"},
		},
		Commands: []*cli.Command{
			{
				Name:   "update",
				Hidden: false,
				Usage:  "Update TDLib Server to the latest release",
				Action: updateServer,
			},
		},
		Copyright:            "Copyright (c) 2024-2025 Pytdbot, AYMENJD",
		Version:              srv.Version,
		EnableBashCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Category: "Configuration",
				Name:     "config",
				Aliases:  []string{"c"},
				Value:    "config.ini",
				Usage:    "Path to the configuration `file`",
			},
			&cli.IntFlag{
				Category: "Logging",
				Name:     "td-verbosity-level",
				Aliases:  []string{"l"},
				Value:    2,
				Usage:    "TDLib verbosity `level`",
			},
			&cli.StringFlag{
				Category: "Logging",
				Name:     "log-file",
				Value:    "",
				Usage:    "Path to log `file` for TDLib logs",
			},
			&cli.BoolFlag{
				Category:    "Logging",
				Name:        "enable-requests-debug",
				DefaultText: "Disabled",
				Usage:       "Enable debugging for requests in TDLib",
			},
		},
		Action: runServer,
	}
}

func runServer(c *cli.Context) error {
	server, err := srv.New(c.Int("td-verbosity-level"), c.String("config"), c.String("log-file"), c.IsSet("enable-requests-debug"))
	if err != nil {
		return err
	}

	// if c.IsSet("enable-requests-debug") {
	// 	server.EnableRequestsDebug(c.Int("td-verbosity-level"))
	// } else {
	// 	server.DisableRequestsDebug()
	// }

	idle := make(chan struct{})
	server.Start()
	registerSignalHandler(server, idle)
	<-idle
	return nil
}

func registerSignalHandler(server *srv.Server, notifyChannel chan struct{}) {
	sigChannel := make(chan os.Signal, 1)
	signal.Notify(sigChannel, syscall.SIGINT, syscall.SIGTERM, syscall.SIGABRT, syscall.SIGSEGV)

	go func() {
		<-sigChannel
		server.Close()
		close(notifyChannel)
	}()
}

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
		Hash               string `json:"digest"`
	} `json:"assets"`
}

func updateServer(c *cli.Context) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current executable path: %v", err)
	}

	currentHash, err := calculateFileSHA256(execPath)
	if err != nil {
		return fmt.Errorf("failed to calculate current binary hash: %v", err)
	}

	resp, err := http.Get("https://api.github.com/repos/pytdbot/tdlib-server/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to check for updates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to check for updates: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read update response: %v", err)
	}

	var release GitHubRelease
	if err := utils.UnmarshalWithTarget(body, &release); err != nil {
		return fmt.Errorf("failed to parse update response: %v", err)
	}

	assetName := fmt.Sprintf("tdlib-server-%s-%s", runtime.GOOS, runtime.GOARCH)
	var (
		downloadURL  string
		fileSize     int64
		upstreamHash string
	)
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			fileSize = asset.Size
			upstreamHash = asset.Hash
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no compatible binary found for your system (%s-%s)", runtime.GOOS, runtime.GOARCH)
	}

	if currentHash == upstreamHash {
		fmt.Println("Already up-to-date")
		return nil
	}

	tempFile := filepath.Join(os.TempDir(), "tdlib-server-update")
	defer os.Remove(tempFile)

	fmt.Printf("Downloading update from %s...\n", downloadURL)
	resp, err = http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download update: HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(tempFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer out.Close()

	bar := pb.Full.Start64(fileSize)
	bar.Set(pb.Bytes, true)
	bar.SetWidth(80)

	reader := bar.NewProxyReader(resp.Body)

	_, err = io.Copy(out, reader)
	if err != nil {
		return fmt.Errorf("failed to write update file: %v", err)
	}

	bar.Finish()

	downloadedHash, err := calculateFileSHA256(tempFile)
	if err != nil {
		return fmt.Errorf("failed to verify downloaded update: %v", err)
	}

	if downloadedHash != upstreamHash {
		return fmt.Errorf("downloaded file hash does not match upstream hash: expected %s, got %s", upstreamHash, downloadedHash)
	}

	err = os.Rename(tempFile, execPath)
	if err != nil {
		return fmt.Errorf("failed to replace binary: %v", err)
	}

	fmt.Printf("Successfully updated to %s version!\n", release.TagName)
	return nil
}

func calculateFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}
