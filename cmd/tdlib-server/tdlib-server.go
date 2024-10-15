package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	srv "github.com/pytdbot/tdlib-server/internal/server"
	"github.com/pytdbot/tdlib-server/internal/utils"

	"github.com/urfave/cli/v2"
)

func main() {
	cli.AppHelpTemplate = `TDLib Server v{{.Version}} - A high-performance Go server built to scale Telegram bots using TDLib and RabbitMQ

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
		Name:  "TDLib Server",
		Usage: "A high-performance TDLib Server meant for scaling bots",
		Authors: []*cli.Author{
			{Name: "AYMEN ~ https://github.com/AYMENJD"},
		},
		Copyright:            "Copyright (c) 2024 Pytdbot, AYMENJD",
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
		sig := <-sigChannel
		fmt.Println("Received signal:", sig)
		server.Close()
		close(notifyChannel)
	}()
}
