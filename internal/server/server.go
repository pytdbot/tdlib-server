package server

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gopkg.in/ini.v1"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

type Data = map[string]interface{}

type Server struct {
	config    *ini.File
	td        *tdjson.TdJson
	requestID *utils.IdGenerator

	authState       Data
	connectionState Data
	options         Data
	stateMu         sync.RWMutex

	myID                string
	myIDInt             int64
	tdRequestsInitValue int64
	isAuthorized        bool
	isRunning           bool
	isDebug             bool

	waitForReady  chan struct{}
	waitForClosed chan struct{}

	mqConnection *amqp.Connection
	mqChannel    *amqp.Channel

	updatesQueue  *amqp.Queue
	requestsQueue *amqp.Queue

	scheduler *utils.Scheduler

	results         *utils.SafeResultsMap
	broadcast_types map[string]struct{}

	updates_count  atomic.Int64
	requests_count atomic.Int64
	uptime         time.Time

	closeTimeout time.Duration
}

// New creates and initializes a new Server instance with the specified verbosity level
// and configuration file path. It returns a pointer to the Server and an error if the
// initialization fails.
func New(td_verbosity_level int, config_path string, log_file string, debug bool) (*Server, error) {
	cfg, err := ini.Load(config_path)

	if err != nil {
		return nil, fmt.Errorf("fail to read configuration file "+config_path+": %v", err)
	}

	closeTimeoutSeconds, err := cfg.Section("server").Key("close_timeout").Int()
	if err != nil {
		closeTimeoutSeconds = 0
	}

	listRaw := strings.Split(cfg.Section("server").Key("broadcast_types").String(), ",")

	mapOfTypes := make(map[string]struct{})
	for _, val := range listRaw {
		if v := strings.TrimSpace(val); v != "" {
			mapOfTypes[v] = struct{}{}
		}
	}

	myID := utils.BotIDFromToken(cfg.Section("server").Key("bot_token").String())
	if myID == "" {
		return nil, fmt.Errorf("invalid bot token")
	}

	myIDInt, err := strconv.Atoi(myID)
	if err != nil {
		utils.PanicOnErr(false, "Could not convert bot ID to int", nil, true)
	}

	td := tdjson.NewTdJson(true, td_verbosity_level, log_file)

	tdRequestsInitValue := utils.UnsafeUnmarshal(td.Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"getLogTagVerbosityLevel",
			utils.Params{
				"tag": "td_requests",
			},
		),
	)))

	return &Server{
		config:    cfg,
		td:        td,
		requestID: utils.NewIdGenerator(),
		results:   utils.NewSafeResultsMap(),

		options:             make(Data),
		myID:                myID,
		myIDInt:             int64(myIDInt),
		isDebug:             debug,
		tdRequestsInitValue: tdRequestsInitValue["verbosity_level"].(int64),
		waitForReady:        make(chan struct{}),
		waitForClosed:       make(chan struct{}),
		broadcast_types:     mapOfTypes,
		closeTimeout:        time.Duration(closeTimeoutSeconds) * time.Second,
	}, nil
}

// Close gracefully shuts down the Server, waiting for all operations to complete.
//
// It returns a boolean indicating whether the shutdown was successful and an error
// if the shutdown fails.
func (srv *Server) Close() (bool, error) {

	res, ok := srv.Invoke(utils.MakeObject("close", utils.Params{}))

	if ok {

		var timeoutChannel <-chan time.Time
		if srv.closeTimeout > 0 {
			timeoutChannel = time.After(srv.closeTimeout)
		}

		should_panic := false

		select {
		case <-srv.waitForClosed:
			if srv.isDebug {
				fmt.Println("TDLib closed gracefully.")
			}
		case <-timeoutChannel:
			should_panic = true
			fmt.Println("Timeout waiting for TDLib to send authorizationStateClosed. Sending fake authorizationStateClosed.")

			srv.broadcast(srv.getFakeUpdateAuthClosed())
		}

		srv.setIsRunning(false)
		srv.results.ClearChannels(true)

		srv.mqChannel.QueueDelete(srv.updatesQueue.Name, false, false, false)
		srv.mqChannel.QueueDelete(srv.requestsQueue.Name, false, false, false)

		srv.mqChannel.Close()
		srv.mqConnection.Close()

		srv.scheduler.Close()
		if should_panic {
			panic("TDLib did not close in time")
		}

		return true, nil
	}

	return false, fmt.Errorf(res["message"].(string))
}

func (srv *Server) Start() {

	srv.startRabbitMQ()

	srv.uptime = time.Now()
	srv.scheduler = utils.NewScheduler(filepath.Join(
		srv.config.Section("server").Key("files_directory").String(), "database",
	), srv.sendScheduledEvent)
	srv.scheduler.Start()

	go srv.Invoke(utils.MakeObject("getOption", utils.Params{"name": "version"}))
	go srv.tdListener()
}

// AuthorizationState returns the currant authorization state of the Server.
func (srv *Server) AuthorizationState() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.Unlock()

	return srv.authState
}

// ConnectionState returns the current connection state of the Server.
func (srv *Server) ConnectionState() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.Unlock()

	return srv.connectionState
}

// Options returns the current options of the Server.
func (srv *Server) Options() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.Unlock()

	return srv.options
}

// Invoke sends a request to TDlib and returns the response data
// along with a boolean indicating whether the request was successful.
func (srv *Server) Invoke(request Data) (Data, bool) {
	request_id := strconv.Itoa(srv.requestID.GenerateID())
	request["@extra"] = make(Data)
	request["@extra"].(Data)["request_id"] = request_id

	channel := srv.results.Make(request_id)

	srv.send(request)

	response := <-channel

	if utils.Type(response) == "error" {
		return response, false
	}

	return response, true
}

func (srv *Server) processUpdate(update Data) {
	if srv.isDebug {
		fmt.Println("Received:", utils.UnsafeMarshalWithIndent(update))
	}

	if extra, exists := update["@extra"]; exists { // it's a response
		srv.requests_count.Add(1)

		extraMap := utils.AsMap(extra)

		if routingKey, exists := extraMap["routing_key"]; exists {
			delete(extraMap, "routing_key")
			srv.sendResponse(routingKey.(string), update)
		} else { // local request
			if requestID, ok := extraMap["request_id"].(string); ok {
				if channel, found := srv.results.Get(requestID); found {
					srv.results.SafeSend(channel, update)
					srv.results.Delete(requestID, false)
				}
			}
		}
	} else { // it's an update
		srv.updates_count.Add(1)

		update_type := utils.Type(update)

		switch update_type {

		case "updateOption":
			srv.handleUpdateOption(update)
		case "updateAuthorizationState":
			srv.handleUpdateAuthorizationState(update)
		case "updateConnectionState":
			srv.handleUpdateConnectionState(update)
		case "updateUser":
			srv.handleUpdateUser(update)
		default:
			if _, exists := srv.broadcast_types[update_type]; exists {
				srv.broadcast(update)
			} else {
				srv.sendUpdate(update)
			}
		}
	}
}

func (srv *Server) processRequest(r amqp.Delivery) {

	if r.ReplyTo == "" || !srv.isRunning {
		return // invalid request
	}

	request, err := utils.Unmarshal(string(r.Body))
	if err != nil {
		return
	}

	extra, ok := request["@extra"].(map[string]interface{})
	if !ok {
		return
	}

	switch strings.ToLower(utils.Type(request)) {
	case "close": // ignore close requests and send fake authorizationStateClosing and authorizationStateClosed
		srv.handleCloseRequest(r, extra)
	case "getcurrentstate":
		srv.handleGetCurrentStateRequest(r, extra)
	case "getserverstats":
		srv.handleGetServerStatsRequest(r, extra)
	case "scheduleevent":
		srv.handleScheduleEventRequest(r, request, extra)
	case "cancelscheduledevent":
		srv.handleCancelScheduledEventRequest(r, request, extra)
	default:
		<-srv.waitForReady
		extra["routing_key"] = r.ReplyTo
		srv.send(request)
	}

}

func (srv *Server) handleCloseRequest(r amqp.Delivery, extra Data) {
	srv.sendResponse(r.ReplyTo, utils.MakeObject("ok", utils.Params{"@extra": extra, "@client_id": srv.td.ClientID}))
	srv.sendResponse(r.ReplyTo, srv.getFakeUpdateAuthClosing())
	srv.sendResponse(r.ReplyTo, srv.getFakeUpdateAuthClosed())
}

func (srv *Server) handleGetCurrentStateRequest(r amqp.Delivery, extra Data) {
	state := srv.getCurrentState()
	state["@extra"] = extra
	state["@client_id"] = srv.td.ClientID
	srv.sendResponse(r.ReplyTo, state)
}

func (srv *Server) handleGetServerStatsRequest(r amqp.Delivery, extra Data) {
	stats := srv.getStats()
	stats["@extra"] = extra
	stats["@client_id"] = srv.td.ClientID
	srv.sendResponse(r.ReplyTo, stats)
}

func (srv *Server) handleScheduleEventRequest(r amqp.Delivery, request Data, extra Data) {
	sendAtValue, ok := request["send_at"]
	if !ok {
		srv.sendError(r.ReplyTo, 400, "send_at is required", extra)
		return
	}

	var sendAt int64
	switch v := sendAtValue.(type) {
	case int64:
		sendAt = v
	case float64:
		sendAt = int64(v)
	case int:
		sendAt = int64(v)
	default:
		srv.sendError(r.ReplyTo, 400, "send_at must be a number", extra)
		return
	}

	if sendAt < time.Now().Unix() {
		srv.sendError(r.ReplyTo, 400, "send_at must be in the future", extra)
		return
	}

	var payload string
	if p, exists := request["payload"]; exists {
		var ok bool
		payload, ok = p.(string)
		if !ok {
			srv.sendError(r.ReplyTo, 400, "payload must be a string", extra)
			return
		}
	}

	var name string
	if n, exists := request["name"]; exists {
		name, _ = n.(string)
	}

	<-srv.waitForReady
	eventID, err := srv.scheduler.CreateEvent(name, sendAt, payload)
	if err != nil {
		srv.sendError(r.ReplyTo, 500, "Could not create scheduled event: "+err.Error(), extra)
		return
	}

	srv.sendResponse(r.ReplyTo, utils.MakeObject("scheduledEvent", utils.Params{
		"event_id":   eventID,
		"send_at":    sendAt,
		"@extra":     extra,
		"@client_id": srv.td.ClientID,
	}))
}

func (srv *Server) handleCancelScheduledEventRequest(r amqp.Delivery, request Data, extra Data) {
	rawID, ok := request["event_id"]
	if !ok {
		srv.sendError(r.ReplyTo, 400, "event_id is required", extra)
		return
	}

	var eventID int64
	switch v := rawID.(type) {
	case float64:
		eventID = int64(v)
	case int:
		eventID = int64(v)
	case int64:
		eventID = v
	default:
		srv.sendError(r.ReplyTo, 400, "event_id must be an integer", extra)
		return
	}

	code := srv.scheduler.CancelEvent(eventID)
	if code == 0 {
		srv.sendError(r.ReplyTo, 400, "Event not found", extra)
		return
	}
	if code < 0 {
		srv.sendError(r.ReplyTo, 500, "Failed to cancel event", extra)
	}

	srv.sendResponse(r.ReplyTo, utils.MakeObject("ok", utils.Params{"@extra": extra, "@client_id": srv.td.ClientID}))
}

func (srv *Server) sendScheduledEvent(name string, event_id int64, payload string) {
	<-srv.waitForReady

	srv.sendUpdate(utils.MakeObject("updateScheduledEvent", utils.Params{
		"name":       name,
		"event_id":   event_id,
		"payload":    payload,
		"@client_id": srv.td.ClientID,
	}))
}

func (srv *Server) getCurrentState() Data {
	updates := Data{
		"@type":   "updates",
		"updates": make([]Data, 0, len(srv.options)+2), // 2+ -> authorizationState + connectionState
	}

	for k, v := range srv.options {
		update := Data{
			"@type":      "updateOption",
			"name":       k,
			"value":      v,
			"@client_id": srv.td.ClientID,
		}
		updates["updates"] = append(updates["updates"].([]Data), update)
	}

	updates["updates"] = append(updates["updates"].([]Data), srv.authState)

	updates["updates"] = append(updates["updates"].([]Data), srv.connectionState)
	return updates
}

func (srv *Server) getStats() Data {
	return Data{
		"@type":          "serverStats",
		"my_id":          srv.myIDInt,
		"uptime":         time.Since(srv.uptime).Seconds(),
		"updates_count":  srv.updates_count.Load(),
		"requests_count": srv.requests_count.Load(),
	}
}

func (srv *Server) getFakeUpdateAuthClosing() Data {
	return Data{"@type": "updateAuthorizationState", "authorization_state": Data{"@type": "authorizationStateClosing"}, "@client_id": srv.td.ClientID}
}

func (srv *Server) getFakeUpdateAuthClosed() Data {
	return Data{"@type": "updateAuthorizationState", "authorization_state": Data{"@type": "authorizationStateClosed"}, "@client_id": srv.td.ClientID}
}

func (srv *Server) EnableRequestsDebug(verbosity_level int) {
	srv.td.Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"setLogTagVerbosityLevel",
			utils.Params{
				"tag":                 "td_requests",
				"new_verbosity_level": verbosity_level,
			},
		),
	))
}

func (srv *Server) DisableRequestsDebug() {
	srv.td.Execute(utils.UnsafeMarshal(
		utils.MakeObject(
			"setLogTagVerbosityLevel",
			utils.Params{
				"tag":                 "td_requests",
				"new_verbosity_level": srv.tdRequestsInitValue,
			},
		),
	))
}

func (srv *Server) send(request Data) {
	srv.td.Send(utils.UnsafeMarshal(request))
	if srv.isDebug {
		fmt.Println("Sent:", utils.UnsafeMarshalWithIndent(request))
	}
}

func (srv *Server) setIsRunning(is_running bool) {
	srv.isRunning = is_running
}

// IsRunning returns a boolean indicating whether the Server is currently running.
func (srv *Server) IsRunning() bool {
	return srv.isRunning
}

func (srv *Server) handleUpdateAuthorizationState(update Data) {
	srv.authState = update

	state := utils.Type(utils.AsMap(update["authorization_state"]))

	if state == "authorizationStateReady" {
		close(srv.waitForReady)
	}

	if state == "authorizationStateWaitTdlibParameters" {
		srv_config := srv.config.Section("server")

		use_test_dc, err := srv_config.Key("use_test_dc").Bool()
		utils.PanicOnErr(err, "Invalid use_test_dc: %v", err, true)

		use_file_database, err := srv_config.Key("use_file_database").Bool()
		utils.PanicOnErr(err, "Invalid use_file_database: %v", err, true)

		use_chat_info_database, err := srv_config.Key("use_chat_info_database").Bool()
		utils.PanicOnErr(err, "Invalid use_chat_info_database: %v", err, true)

		use_message_database, err := srv_config.Key("use_message_database").Bool()
		utils.PanicOnErr(err, "Invalid use_message_database: %v", err, true)

		srv.setTdOptions()

		res, ok := srv.Invoke(
			utils.MakeObject(
				"setTdlibParameters",
				utils.Params{
					"use_test_dc":            use_test_dc,
					"api_id":                 srv_config.Key("api_id").String(),
					"api_hash":               srv_config.Key("api_hash").String(),
					"device_model":           runtime.Version() + " " + runtime.GOARCH,
					"use_file_database":      use_file_database,
					"use_chat_info_database": use_chat_info_database,
					"use_message_database":   use_message_database,
					"files_directory":        srv_config.Key("files_directory").String(),
					"database_directory": filepath.Join(
						srv_config.Key("files_directory").String(), "database",
					),
					"system_language_code": srv_config.Key("system_language_code").String(),
					"database_encryption_key": base64.StdEncoding.EncodeToString([]byte(
						srv_config.Key("database_encryption_key").String(),
					)),
					"application_version": AppName + " v" + Version,
				},
			),
		)

		utils.PanicOnErr(ok, "Could not set TDLib parameters: %v", res["message"], true)

	} else if state == "authorizationStateWaitPhoneNumber" {
		res, ok := srv.Invoke(
			utils.MakeObject(
				"checkAuthenticationBotToken",
				utils.Params{
					"token": srv.config.Section("server").Key("bot_token").String(),
				},
			),
		)

		utils.PanicOnErr(ok, "Could not set bot token: %v", res["message"], true)

	} else if state == "authorizationStateReady" {
		srv.isAuthorized = true
	} else if state == "authorizationStateLoggingOut" || state == "authorizationStateClosing" {
		srv.isAuthorized = false
	} else if state == "authorizationStateClosed" {
		srv.isAuthorized = false
		close(srv.waitForClosed)
	}

	srv.broadcast(update)
}

func (srv *Server) handleUpdateConnectionState(connectionState Data) {
	srv.connectionState = connectionState
	srv.broadcast(connectionState)
}

func (srv *Server) handleUpdateOption(option Data) {
	srv.stateMu.Lock()
	srv.options[option["name"].(string)] = option["value"]
	srv.stateMu.Unlock()

	srv.broadcast(option)
}

func (srv *Server) handleUpdateUser(user Data) {
	if utils.AsMap(user["user"])["id"].(int64) == srv.myIDInt {
		srv.broadcast(user)
	} else {
		srv.sendUpdate(user)
	}
}

func (srv *Server) setTdOptions() {
	for _, key := range srv.config.Section("options").Keys() {
		val := key.String()
		var optionType string
		var value interface{}

		switch {
		case utils.IsBool(val):
			optionType = "optionValueBoolean"
			boolVal, _ := strconv.ParseBool(val)
			value = boolVal
		case utils.IsInt(val):
			optionType = "optionValueInteger"
			intVal, _ := strconv.ParseInt(val, 10, 64)
			value = intVal
		default:
			optionType = "optionValueString"
			value = val
		}

		srv.send(utils.MakeObject("setOption", utils.Params{
			"name":   key.Name(),
			"value":  utils.MakeObject(optionType, utils.Params{"value": value}),
			"@extra": Data{"option": key.Name(), "value": value},
		}))
	}
}

func (srv *Server) tdListener() {
	srv.setIsRunning(true)
	defer srv.setIsRunning(false)

	for srv.isRunning {
		res := srv.td.Receive(1000.0)
		if res == "" {
			continue
		}

		go srv.processUpdate(utils.UnsafeUnmarshal(res))

	}
}

func (srv *Server) startRabbitMQ() {
	rb_config := srv.config.Section("rabbitmq")

	username := url.QueryEscape(rb_config.Key("username").String())
	password := url.QueryEscape(rb_config.Key("password").String())
	host := rb_config.Key("host").String()
	port := rb_config.Key("port").String()
	delete_on_startup, _ := rb_config.Key("delete_on_startup").Bool()

	connection, err := amqp.Dial("amqp://" + username + ":" + password + "@" + host + ":" + port + "/")
	utils.PanicOnErr(err, "Could not connect to RabbitMQ: %v", err, true)

	channel, err := connection.Channel()
	utils.PanicOnErr(err, "Could not open a Channel: %v", err, true)

	channel.ExchangeDeclare(
		srv.myID+"_broadcast", // exchange name
		"fanout",              // exchange type
		false,                 // durable
		false,                 // auto-deleted
		false,                 // internal
		false,                 // no-wait
		nil,                   // arguments
	)

	if delete_on_startup {
		channel.QueueDelete(srv.myID+"_updates", false, false, false)
	}

	updatesQueue, err := channel.QueueDeclare(
		srv.myID+"_updates", // name
		false,               // durable
		false,               // delete when unused
		false,               // exclusive
		false,               // no-wait
		nil,                 // arguments
	)
	utils.PanicOnErr(err, "Could not declare updates queue: %v", err, false)

	srv.updatesQueue = &updatesQueue

	if delete_on_startup {
		channel.QueueDelete(srv.myID+"_requests", false, false, false)
	}

	requestsQueue, err := channel.QueueDeclare(
		srv.myID+"_requests", // name
		false,                // durable
		false,                // delete when unused
		false,                // exclusive
		false,                // no-wait
		nil,                  // arguments
	)
	utils.PanicOnErr(err, "Could not declare requests queue: %v", err, false)
	srv.requestsQueue = &requestsQueue

	srv.mqConnection = connection
	srv.mqChannel = channel

	go srv.requestsListener()
}

func (srv *Server) requestsListener() {
	requests, err := srv.mqChannel.Consume(
		srv.requestsQueue.Name, // Queue name
		AppName,                // Consumer tag
		true,                   // Auto-ack
		true,                   // Exclusive
		false,                  // No-local
		true,                   // No-wait
		nil,                    // Additional arguments
	)
	utils.PanicOnErr(err, "Could not consume requests queue: %v", err, false)

	for request := range requests {
		go srv.processRequest(request)
	}
}

func (srv *Server) sendError(routing_key string, code int, message string, extra Data) {
	srv.sendResponse(routing_key, utils.MakeObject("error", utils.Params{
		"code":       code,
		"message":    message,
		"@extra":     extra,
		"@client_id": srv.td.ClientID,
	}))
}

func (srv *Server) sendResponse(routing_key string, update Data) {
	srv.mqChannel.Publish(
		"",          // exchange
		routing_key, // routing key
		false,       // mandatory
		false,       // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(utils.UnsafeMarshal(update)),
		},
	)
}

func (srv *Server) sendUpdate(update Data) {
	err := srv.mqChannel.Publish(
		"",                    // exchange
		srv.updatesQueue.Name, // routing key
		false,                 // mandatory
		false,                 // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(utils.UnsafeMarshal(update)),
		},
	)
	utils.PanicOnErr(err, "Could not publish message: %v", err, false)
}

func (srv *Server) broadcast(update Data) {
	err := srv.mqChannel.Publish(
		"broadcast", // exchange
		"",          // routing key
		false,       // mandatory
		false,       // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(utils.UnsafeMarshal(update)),
		},
	)
	utils.PanicOnErr(err, "Could not publish broadcasted message: %v", err, false)
}
