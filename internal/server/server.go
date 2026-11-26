package server

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nats "github.com/nats-io/nats.go"

	"gopkg.in/ini.v1"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

type Data = map[string]interface{}

type PublishRequest struct {
	exchange   string
	routingKey string
	body       []byte
}

type Server struct {
	config    *ini.File
	td        *tdjson.TdJson
	tdSend    func(request string) // hands a request to TDLib; tests substitute it
	requestID *utils.IdGenerator

	authState       Data
	connectionState Data
	options         Data
	stateMu         sync.RWMutex

	myID                string
	myIDInt             int64
	tdRequestsInitValue int64
	isAuthorized        atomic.Bool
	isRunning           atomic.Bool
	isDebug             bool

	waitForReady  chan struct{}
	waitForClosed chan struct{}
	readyOnce     sync.Once
	closedOnce    sync.Once

	natsConn    *nats.Conn
	natsPublish func(subject string, body []byte) error // sends to NATS; tests substitute it

	updatesSubject   string
	requestsSubject  string
	broadcastSubject string

	results         *utils.SafeResultsMap
	broadcast_types map[string]struct{}

	updates_count  atomic.Int64
	requests_count atomic.Int64
	uptime         time.Time

	closeTimeout time.Duration

	databaseDirectory string

	publishDropped atomic.Int64
}

// New creates and initializes a new Server instance with the specified verbosity level
// and configuration file path. It returns a pointer to the Server and an error if the
// initialization fails.
func New(td_verbosity_level int, config_path string, log_file string, debug bool) (*Server, error) {
	cfg, err := ini.Load(config_path)

	if err != nil {
		return nil, fmt.Errorf("fail to read configuration file %s: %v", config_path, err)
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
		utils.PanicOnErr(err, "Could not convert bot ID to int: %v", err, true)
	}

	td := tdjson.NewTdJson(true, td_verbosity_level, log_file)

	databaseDirectory := filepath.Join(cfg.Section("server").Key("files_directory").String(), "database")

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
		tdSend:    td.Send,
		requestID: utils.NewIdGenerator(),
		results:   utils.NewSafeResultsMap(),

		updatesSubject:   "bot." + myID + ".updates",
		requestsSubject:  "bot." + myID + ".requests",
		broadcastSubject: "bot." + myID + ".broadcast",

		options:             make(Data),
		myID:                myID,
		myIDInt:             int64(myIDInt),
		isDebug:             debug,
		tdRequestsInitValue: tdRequestsInitValue["verbosity_level"].(int64),
		waitForReady:        make(chan struct{}),
		waitForClosed:       make(chan struct{}),
		broadcast_types:     mapOfTypes,
		closeTimeout:        time.Duration(closeTimeoutSeconds) * time.Second,
		databaseDirectory:   databaseDirectory,
	}, nil
}

// Close gracefully shuts down the Server, waiting for all operations to complete.
//
// It returns a boolean indicating whether the shutdown was successful and an error
// if the shutdown fails.
func (srv *Server) Close() (bool, error) {
	res, ok := srv.Invoke(utils.MakeObject("close", utils.Params{}))

	if !ok {
		return false, fmt.Errorf("%s", res["message"].(string))
	}

	var timeoutChannel <-chan time.Time

	if srv.closeTimeout > 0 {
		timeoutChannel = time.After(srv.closeTimeout)
	}

	shouldPanic := false

	select {
	case <-srv.waitForClosed:
		if srv.isDebug {
			fmt.Println("TDLib closed gracefully.")
		}
	case <-timeoutChannel:
		shouldPanic = true

		fmt.Println(
			"Timeout waiting for TDLib authorizationStateClosed. Sending fake closed update.",
		)

		srv.broadcast(srv.getFakeUpdateAuthClosed())
	}

	srv.setIsRunning(false)

	srv.results.Clear()

	if srv.natsConn != nil {
		err := srv.natsConn.Drain()

		if err != nil {
			fmt.Printf("NATS drain error: %v\n", err)
		}

		srv.natsConn.Close()
	}

	if shouldPanic {
		panic("TDLib did not close in time")
	}

	return true, nil
}

func (srv *Server) Start() {
	srv.startNATS()

	srv.uptime = time.Now()

	go srv.Invoke(utils.MakeObject("getOption", utils.Params{"name": "version"}))
	go srv.tdListener()
}

// AuthorizationState returns the currant authorization state of the Server.
func (srv *Server) AuthorizationState() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.RUnlock()

	return srv.authState
}

// ConnectionState returns the current connection state of the Server.
func (srv *Server) ConnectionState() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.RUnlock()

	return srv.connectionState
}

// Options returns the current options of the Server.
func (srv *Server) Options() Data {
	srv.stateMu.RLock()
	defer srv.stateMu.RUnlock()

	return srv.options
}

// processUpdate handles an update or a response, still as the JSON TDLib
// produced. Updates are relayed as those very bytes: nothing is decoded and
// encoded again on the way to NATS. Only the kinds of update the server acts on,
// and responses to the server's own requests, are decoded.
func (srv *Server) processUpdate(raw []byte) {
	if srv.isDebug {
		fmt.Println("Received:", string(raw))
	}

	valStart, valEnd, isResponse, seen := locateExtra(raw)
	if seen {
		if !isResponse {
			srv.processDecoded(utils.UnsafeUnmarshalBytes(raw)) // not TDLib's usual layout
			return
		}

		srv.requests_count.Add(1) // it's a response

		if reply, body, ok := stripRoutingKey(raw, valStart, valEnd); ok { // for a client
			srv.publish(reply, body)
			return
		}
		srv.deliverLocal(raw) // for an Invoke() of the server's own
		return
	}

	srv.updates_count.Add(1) // it's an update

	switch t := jsonType(raw); {
	case eq(t, "updateOption"):
		srv.handleUpdateOption(utils.UnsafeUnmarshalBytes(raw))
	case eq(t, "updateAuthorizationState"):
		// Blocks in Invoke() on a response only tdListener can receive.
		go srv.handleUpdateAuthorizationState(utils.UnsafeUnmarshalBytes(raw))
	case eq(t, "updateConnectionState"):
		srv.handleUpdateConnectionState(utils.UnsafeUnmarshalBytes(raw))
	case eq(t, "updateUser"):
		subject := srv.updatesSubject
		if id, ok := jsonNestedInt(raw, `"user"`, `"id"`); ok && id == srv.myIDInt {
			subject = srv.broadcastSubject
		}
		srv.publish(subject, raw)
	default:
		if _, exists := srv.broadcast_types[string(t)]; exists {
			srv.publish(srv.broadcastSubject, raw)
		} else {
			srv.publish(srv.updatesSubject, raw)
		}
	}
}

// processDecoded is processUpdate for a message that is already a Data, which
// only messages in a layout TDLib does not usually produce still need.
func (srv *Server) processDecoded(update Data) {
	if srv.isDebug {
		fmt.Println("Received:", utils.UnsafeMarshalWithIndent(update))
	}

	if extra, exists := update["@extra"]; exists { // it's a response
		srv.requests_count.Add(1)

		extraMap := utils.AsMap(extra)

		if routingKey, ok := extraMap["routing_key"].(string); ok {
			delete(extraMap, "routing_key")
			srv.sendResponse(routingKey, update)
		} else { // local request
			if requestID, ok := extraMap["request_id"].(string); ok {
				if channel, found := srv.results.Get(requestID); found {
					srv.results.SafeSend(channel, update)
					srv.results.Delete(requestID)
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
			// Blocks in Invoke() on a response only tdListener can receive.
			go srv.handleUpdateAuthorizationState(update)
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

// deliverLocal hands a response to the Invoke() call waiting for it.
func (srv *Server) deliverLocal(raw []byte) {
	update := utils.UnsafeUnmarshalBytes(raw)

	if requestID, ok := utils.AsMap(update["@extra"])["request_id"].(string); ok {
		if channel, found := srv.results.Get(requestID); found {
			srv.results.SafeSend(channel, update)
			srv.results.Delete(requestID)
		}
	}
}

// forwardRequest hands a regular request to TDLib as the bytes the client sent,
// with "routing_key" added to its "@extra", and reports true. It reports false
// when the request is one the server answers itself or is too unusual to be
// handled without decoding; such requests take the road they always did.
func (srv *Server) forwardRequest(r *nats.Msg) bool {
	extraStart, extraEnd, typ, hasExtra := scanRequest(r.Data)
	switch {
	case eq(typ, "close"), eq(typ, "getCurrentState"), eq(typ, "getServerStats"):
		return false
	case !hasExtra:
		return false // not a well-formed object with "@extra": dropped below, as before
	case r.Data[extraStart] != '{':
		return true // "@extra" is not an object: ignored, as before
	case !jsonSafe(r.Reply):
		return false // needs escaping: let an encoder write it
	}

	select {
	case <-srv.waitForReady:
	case <-srv.waitForClosed:
		return true
	}

	body, ok := spliceRoutingKey(r.Data, extraStart, extraEnd, r.Reply)
	if !ok {
		return false
	}

	srv.tdSend(bytesToString(body))
	if srv.isDebug {
		fmt.Println("Sent:", string(body))
	}
	return true
}

// Invoke sends a request to TDlib and returns the response data
// along with a boolean indicating whether the request was successful.
func (srv *Server) Invoke(request Data) (Data, bool) {
	request_id := strconv.FormatInt(srv.requestID.GenerateID(), 10)

	request["@extra"] = Data{"request_id": request_id}

	channel := srv.results.Make(request_id)

	srv.send(request)

	select {

	case response := <-channel:

		if utils.Type(response) == "error" {
			return response, false
		}

		return response, true

	case <-srv.waitForClosed:

		srv.results.Delete(request_id)

		return utils.MakeError(500, "TDLib closed"), false
	}
}

func (srv *Server) processRequest(r *nats.Msg) {

	if r.Reply == "" || !srv.isRunning.Load() {
		return // invalid request
	}

	if srv.forwardRequest(r) {
		return
	}

	request, err := utils.UnmarshalBytes(r.Data)
	if err != nil {
		return
	}

	extra, ok := request["@extra"].(map[string]interface{})
	if !ok {
		return
	}

	switch utils.Type(request) {
	case "close":
		srv.handleCloseRequest(r, extra) // ignore close requests; returns OK immediately
	case "getCurrentState":
		srv.handleGetCurrentStateRequest(r, extra)
	case "getServerStats":
		srv.handleGetServerStatsRequest(r, extra)
	default:
		select {
		case <-srv.waitForReady:
		case <-srv.waitForClosed:
			return
		}
		extra["routing_key"] = r.Reply
		srv.send(request)
	}
}

func (srv *Server) handleCloseRequest(r *nats.Msg, extra Data) {
	srv.sendResponse(r.Reply, utils.MakeObject("ok", utils.Params{"@extra": extra, "@client_id": srv.td.ClientID}))
}

func (srv *Server) handleGetCurrentStateRequest(r *nats.Msg, extra Data) {
	state := srv.getCurrentState()
	state["@extra"] = extra
	state["@client_id"] = srv.td.ClientID
	srv.sendResponse(r.Reply, state)
}

func (srv *Server) handleGetServerStatsRequest(r *nats.Msg, extra Data) {
	stats := srv.getStats()
	stats["@extra"] = extra
	stats["@client_id"] = srv.td.ClientID
	srv.sendResponse(r.Reply, stats)
}

func (srv *Server) getCurrentState() Data {
	updateSlice := make([]Data, 0, len(srv.options)+2) // 2+ -> authorizationState + connectionState

	for k, v := range srv.options {
		updateSlice = append(updateSlice, Data{
			"@type":      "updateOption",
			"name":       k,
			"value":      v,
			"@client_id": srv.td.ClientID,
		})
	}

	updateSlice = append(updateSlice, srv.authState, srv.connectionState)
	return Data{
		"@type":      "updates",
		"updates":    updateSlice,
		"@client_id": srv.td.ClientID,
	}
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
	srv.tdSend(utils.UnsafeMarshal(request))
	if srv.isDebug {
		fmt.Println("Sent:", utils.UnsafeMarshalWithIndent(request))
	}
}

func (srv *Server) setIsRunning(is_running bool) {
	srv.isRunning.Store(is_running)
}

// IsRunning returns a boolean indicating whether the Server is currently running.
func (srv *Server) IsRunning() bool {
	return srv.isRunning.Load()
}

func (srv *Server) handleUpdateAuthorizationState(update Data) {

	srv.stateMu.Lock()
	srv.authState = update
	srv.stateMu.Unlock()

	state := utils.Type(utils.AsMap(update["authorization_state"]))

	if state == "authorizationStateReady" {
		srv.readyOnce.Do(func() { close(srv.waitForReady) })
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
					"database_directory":     srv.databaseDirectory,
					"system_language_code":   srv_config.Key("system_language_code").String(),
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

		srv.isAuthorized.Store(true)

	} else if state == "authorizationStateLoggingOut" ||
		state == "authorizationStateClosing" {

		srv.isAuthorized.Store(false)

	} else if state == "authorizationStateClosed" {

		srv.isAuthorized.Store(false)
		srv.closedOnce.Do(func() { close(srv.waitForClosed) })
	}

	srv.broadcast(update)
}

func (srv *Server) handleUpdateConnectionState(connectionState Data) {
	srv.stateMu.Lock()
	srv.connectionState = connectionState
	srv.stateMu.Unlock()

	srv.broadcast(connectionState)
}

func (srv *Server) handleUpdateOption(option Data) {
	if name, ok := option["name"].(string); ok {
		srv.stateMu.Lock()
		srv.options[name] = option["value"]
		srv.stateMu.Unlock()
	}

	srv.broadcast(option)
}

func (srv *Server) handleUpdateUser(user Data) {
	if id, ok := utils.AsMap(user["user"])["id"].(int64); ok && id == srv.myIDInt {
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

		if boolVal, err := strconv.ParseBool(val); err == nil {
			optionType = "optionValueBoolean"
			value = boolVal
		} else if intVal, err := strconv.ParseInt(val, 10, 64); err == nil {
			optionType = "optionValueInteger"
			value = intVal
		} else {
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

// updateQueueSize bounds how many messages may wait for NATS. A full queue
// means TDLib is outpacing NATS, and receiving pauses until the backlog has
// shrunk: back-pressure instead of memory growing without limit.
const updateQueueSize = 1024

func (srv *Server) tdListener() {
	srv.setIsRunning(true)
	defer srv.setIsRunning(false)

	// One worker publishes what TDLib produced, in that order. Receiving is
	// thereby never held up by a publish, and nothing piles up without bound,
	// as a goroutine per message did. Messages stay the bytes TDLib produced:
	// nothing is decoded and encoded again on the way to NATS.
	queue := make(chan []byte, updateQueueSize)
	defer close(queue)
	go func() {
		for raw := range queue {
			srv.processUpdate(raw)
		}
	}()

	for srv.isRunning.Load() {
		raw := srv.td.ReceiveBytes(1000.0)
		if len(raw) == 0 {
			continue
		}

		queue <- raw
	}
}

func (srv *Server) startNATS() {
	cfg := srv.config.Section("nats")

	url := cfg.Key("url").MustString(nats.DefaultURL)

	nc, err := nats.Connect(
		url,

		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),

		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			fmt.Printf("NATS disconnected: %v\n", err)
		}),

		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Println("NATS reconnected.")
		}),

		nats.ClosedHandler(func(nc *nats.Conn) {
			fmt.Println("NATS connection closed.")
		}),
	)

	utils.PanicOnErr(err, "Could not connect to NATS: %v", err, true)

	srv.natsConn = nc
	srv.natsPublish = nc.Publish

	srv.requestsListener()
}

func (srv *Server) requestsListener() {
	_, err := srv.natsConn.Subscribe(
		srv.requestsSubject,
		func(msg *nats.Msg) {
			go srv.processRequest(msg)
		},
	)

	if err != nil {
		fmt.Printf("Could not subscribe to requests: %v\n", err)
		return
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

func (srv *Server) sendResponse(reply string, update Data) {
	if srv.isRunning.Load() {
		srv.publish(reply, utils.UnsafeMarshalBytes(update))
	}
}

func (srv *Server) sendUpdate(update Data) {

	if srv.isRunning.Load() {
		srv.publish(srv.updatesSubject, utils.UnsafeMarshalBytes(update))
	}
}

func (srv *Server) broadcast(update Data) {

	if srv.isRunning.Load() {
		srv.publish(srv.broadcastSubject, utils.UnsafeMarshalBytes(update))
	}
}

// publish sends body to a NATS subject unchanged.
func (srv *Server) publish(subject string, body []byte) {
	if !srv.isRunning.Load() {
		return
	}

	if err := srv.natsPublish(subject, body); err != nil {
		fmt.Printf("Failed publish to %s: %v\n", subject, err)
	}
}
