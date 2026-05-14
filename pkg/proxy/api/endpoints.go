package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/multiversx/mx-chain-core-go/marshal"
	"github.com/multiversx/mx-chain-go/api/logs"
	"github.com/multiversx/mx-chain-go/node/chainSimulator/dtos"
	logger "github.com/multiversx/mx-chain-logger-go"
	"github.com/multiversx/mx-chain-proxy-go/api/shared"
	"github.com/multiversx/mx-chain-proxy-go/data"
	dtosc "github.com/multiversx/mx-chain-simulator-go/pkg/dtos"
)

var log = logger.GetOrCreate("pkg/proxy/api")

const (
	generateBlocksEndpoint                  = "/simulator/generate-blocks/:num"
	generateBlocksUntilEpochReached         = "/simulator/generate-blocks-until-epoch-reached/:epoch"
	generateBlocksUntilTransactionProcessed = "/simulator/generate-blocks-until-transaction-processed/:txHash"
	initialWalletsEndpoint                  = "/simulator/initial-wallets"
	setKeyValuesEndpoint                    = "/simulator/address/:address/set-state"
	setStateMultipleEndpoint                = "/simulator/set-state"
	setStateMultipleOverwriteEndpoint       = "/simulator/set-state-overwrite"
	addValidatorsKeys                       = "/simulator/add-keys"
	forceUpdateValidatorStatistics          = "/simulator/force-reset-validator-statistics"
	observersInfo                           = "/simulator/observers"
	epochChange                             = "/simulator/force-epoch-change"

	queryParamNoGenerate   = "noGenerate"
	queryParamTargetEpoch  = "targetEpoch"
	queryParamMaxNumBlocks = "maxNumBlocks"

	maxNumOfBlockToGenerateUntilTxProcessed = 20
	maxSimulatorRequestBodySize             = 10 << 20
	maxStateEntries                         = 1024
	maxValidatorKeys                        = 400
	maxLogStreams                           = int32(64)
)

var activeLogStreams int32

type endpointsProcessor struct {
	facade SimulatorFacade
}

// NewEndpointsProcessor will create a new instance of endpointsProcessor
func NewEndpointsProcessor(facade SimulatorFacade) (*endpointsProcessor, error) {
	return &endpointsProcessor{
		facade: facade,
	}, nil
}

// ExtendProxyServer will extend the proxy server with extra endpoints
func (ep *endpointsProcessor) ExtendProxyServer(httpServer *http.Server) error {
	ws, ok := httpServer.Handler.(*gin.Engine)
	if !ok {
		return errors.New("cannot cast httpServer.Handler to gin.Engine")
	}

	// ISSUE-004 layer 2: opt-in Bearer-token auth on every endpoint.
	// No-op when MX_CHAIN_SIMULATOR_AUTH_TOKEN is unset/empty (preserves
	// the historical zero-auth behavior so existing dev/CI workflows that
	// rely on loopback-bind safety continue to work). When set, the
	// middleware gates every method on every route registered against
	// this engine — including GET /simulator/initial-wallets, which
	// returns WalletKey.PrivateKeyHex and therefore must NOT be exempt
	// (regression closure: the previous "safe-method exemption" leaked
	// initial wallet keys to any unauthenticated caller).
	ws.Use(newAuthMiddleware())

	ws.POST(generateBlocksEndpoint, ep.generateBlocks)
	ws.POST(generateBlocksUntilEpochReached, ep.generateBlocksUntilEpochReached)
	ws.POST(generateBlocksUntilTransactionProcessed, ep.generateBlocksUntilTransactionProcessed)
	ws.GET(initialWalletsEndpoint, ep.initialWallets)
	ws.POST(setKeyValuesEndpoint, ep.setKeyValue)
	ws.POST(setStateMultipleEndpoint, ep.setStateMultiple)
	ws.POST(setStateMultipleOverwriteEndpoint, ep.setStateMultipleOverwrite)
	ws.POST(addValidatorsKeys, ep.addValidatorKeys)
	ws.POST(forceUpdateValidatorStatistics, ep.forceUpdateValidatorStatistics)
	ws.GET(observersInfo, ep.getObserversInfo)
	ws.POST(epochChange, ep.forceEpochChange)

	serializerForLogs := &marshal.GogoProtoMarshalizer{}
	registerLoggerWsRoute(ws, serializerForLogs)

	return nil
}

// registerLoggerWsRoute will register the log route
func registerLoggerWsRoute(ws *gin.Engine, serializer marshal.Marshalizer) {
	// ISSUE-029: migrated from github.com/btcsuite/websocket to
	// github.com/gorilla/websocket so the whole stack uses the same
	// WebSocket implementation (gorilla is what notifier and chain-go
	// already use). Same API, fewer parser quirks to track. Also build
	// the upgrader ONCE with HandshakeTimeout + the strict origin check
	// (the previous code re-assigned CheckOrigin inside the handler on
	// every connection, which works but is wasteful).
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		CheckOrigin:      isAllowedWebSocketOrigin,
	}

	ws.GET("/log", func(c *gin.Context) {
		if atomic.AddInt32(&activeLogStreams, 1) > maxLogStreams {
			atomic.AddInt32(&activeLogStreams, -1)
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many log streams"})
			return
		}
		defer atomic.AddInt32(&activeLogStreams, -1)

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Error(err.Error())
			return
		}

		ls, err := logs.NewLogSender(serializer, conn, log)
		if err != nil {
			log.Error(err.Error())
			return
		}

		ls.StartSendingBlocking()
	})
}

func (ep *endpointsProcessor) forceEpochChange(c *gin.Context) {
	targetEpoch, err := getTargetEpochQueryParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if targetEpoch < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "targetEpoch must not be negative"})
		return
	}

	err = ep.facade.ForceChangeOfEpoch(uint32(targetEpoch))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func getTargetEpochQueryParam(c *gin.Context) (int, error) {
	epochStr := c.Request.URL.Query().Get(queryParamTargetEpoch)
	if epochStr == "" {
		return 0, nil
	}

	epoch, err := strconv.Atoi(epochStr)
	if err != nil {
		shared.RespondWithBadRequest(c, "cannot convert string to number")
		return 0, errors.New("cannot convert string to number")
	}

	return epoch, nil
}

func (ep *endpointsProcessor) generateBlocks(c *gin.Context) {
	numStr := c.Param("num")
	if numStr == "" {
		shared.RespondWithBadRequest(c, "invalid number of blocks")
		return
	}

	num, err := strconv.Atoi(numStr)
	if err != nil {
		shared.RespondWithBadRequest(c, "cannot convert string to number")
		return
	}

	err = ep.facade.GenerateBlocks(num)
	if err != nil {
		shared.RespondWithInternalError(c, errors.New("cannot generate blocks"), err)
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) generateBlocksUntilEpochReached(c *gin.Context) {
	epochStr := c.Param("epoch")
	if epochStr == "" {
		shared.RespondWithBadRequest(c, "invalid epoch")
		return
	}

	epoch, err := strconv.Atoi(epochStr)
	if err != nil {
		shared.RespondWithBadRequest(c, "cannot convert string to number")
		return
	}

	err = ep.facade.GenerateBlocksUntilEpochIsReached(int32(epoch))
	if err != nil {
		shared.RespondWithInternalError(c, errors.New("cannot generate blocks"), err)
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) generateBlocksUntilTransactionProcessed(c *gin.Context) {
	txHashStr := c.Param("txHash")

	maxNumBlocks, err := getMaxNumBlocksToGenerate(c)
	if err != nil {
		shared.RespondWithBadRequest(c, err.Error())
		return
	}

	err = ep.facade.GenerateBlocksUntilTransactionIsProcessed(txHashStr, maxNumBlocks)
	if err != nil {
		shared.RespondWithInternalError(c, errors.New("cannot generate blocks"), err)
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) getObserversInfo(c *gin.Context) {
	observersData, err := ep.facade.GetObserversInfo()
	if err != nil {
		shared.RespondWithInternalError(c, errors.New("cannot get observers info"), err)
		return
	}

	shared.RespondWith(c, http.StatusOK, observersData, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) initialWallets(c *gin.Context) {
	initialWallets := ep.facade.GetInitialWalletKeys()

	shared.RespondWith(c, http.StatusOK, initialWallets, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) setKeyValue(c *gin.Context) {
	address := c.Param("address")
	if address == "" {
		shared.RespondWithBadRequest(c, "invalid provided address")
		return
	}

	// ISSUE-018: gate body size before ShouldBindJSON. Sibling mutators
	// (setStateMultiple, setStateMultipleOverwrite, addValidatorKeys)
	// all do this; setKeyValue used to skip it, letting a multi-GiB POST
	// drain memory inside the JSON parser.
	if !limitRequestBody(c) {
		return
	}

	var keyValueMap = map[string]string{}
	err := c.ShouldBindJSON(&keyValueMap)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid key value map, error: %s", err.Error()))
		return
	}

	err = ep.facade.SetKeyValueForAddress(address, keyValueMap)
	if err != nil {
		shared.RespondWithInternalError(c, errors.New("cannot set key value pairs"), err)
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func getQueryParamNoGenerate(c *gin.Context) (bool, error) {
	withResultsStr := c.Request.URL.Query().Get(queryParamNoGenerate)
	if withResultsStr == "" {
		return false, nil
	}

	return strconv.ParseBool(withResultsStr)
}

func getMaxNumBlocksToGenerate(c *gin.Context) (int, error) {
	withResultsStr := c.Request.URL.Query().Get(queryParamMaxNumBlocks)
	if withResultsStr == "" {
		return maxNumOfBlockToGenerateUntilTxProcessed, nil
	}

	value, err := strconv.Atoi(withResultsStr)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value", queryParamMaxNumBlocks)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", queryParamMaxNumBlocks)
	}
	if value > maxNumOfBlockToGenerateUntilTxProcessed {
		return 0, fmt.Errorf("%s must be at most %d", queryParamMaxNumBlocks, maxNumOfBlockToGenerateUntilTxProcessed)
	}

	return value, nil
}

func isAllowedWebSocketOrigin(r *http.Request) bool {
	// ISSUE-029: previously empty Origin was accepted unconditionally,
	// allowing non-browser clients without browser CSRF protection.
	// Reject empty Origin: simulator /log is loopback-only by default
	// (cmd/chainsimulator/main.go:164) and any local consumer can set
	// a same-host Origin header trivially.
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}

	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return false
	}

	return parsedOrigin.Host == r.Host
}

func (ep *endpointsProcessor) setStateMultiple(c *gin.Context) {
	var stateSlice []*dtos.AddressState

	noGenerate, err := getQueryParamNoGenerate(c)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid query parameter %s, error: %s", queryParamNoGenerate, err.Error()))
		return
	}

	if !limitRequestBody(c) {
		return
	}
	err = c.ShouldBindJSON(&stateSlice)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid state structure, error: %s", err.Error()))
		return
	}
	if len(stateSlice) > maxStateEntries {
		shared.RespondWithBadRequest(c, "too many state entries")
		return
	}

	err = ep.facade.SetStateMultiple(stateSlice, noGenerate)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("cannot set state, error: %s", err.Error()))
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) setStateMultipleOverwrite(c *gin.Context) {
	noGenerate, err := getQueryParamNoGenerate(c)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid query parameter %s, error: %s", queryParamNoGenerate, err.Error()))
		return
	}

	var stateSlice []*dtos.AddressState
	if !limitRequestBody(c) {
		return
	}
	err = c.ShouldBindJSON(&stateSlice)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid state structure, error: %s", err.Error()))
		return
	}
	if len(stateSlice) > maxStateEntries {
		shared.RespondWithBadRequest(c, "too many state entries")
		return
	}

	err = ep.facade.SetStateMultipleOverwrite(stateSlice, noGenerate)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("cannot overwrite state, error: %s", err.Error()))
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func (ep *endpointsProcessor) addValidatorKeys(c *gin.Context) {
	validatorsKeys := &dtosc.ValidatorKeys{}

	if !limitRequestBody(c) {
		return
	}
	err := c.ShouldBindJSON(validatorsKeys)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("invalid validators keys structure, error: %s", err.Error()))
		return
	}
	if len(validatorsKeys.PrivateKeysBase64) > maxValidatorKeys {
		shared.RespondWithBadRequest(c, "too many validator keys")
		return
	}

	err = ep.facade.AddValidatorKeys(validatorsKeys)
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("cannot add validator keys, error: %s", err.Error()))
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}

func limitRequestBody(c *gin.Context) bool {
	if c.Request.ContentLength > maxSimulatorRequestBodySize {
		shared.RespondWithBadRequest(c, "request body too large")
		return false
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSimulatorRequestBodySize)
	return true
}

func (ep *endpointsProcessor) forceUpdateValidatorStatistics(c *gin.Context) {
	err := ep.facade.ForceUpdateValidatorStatistics()
	if err != nil {
		shared.RespondWithBadRequest(c, fmt.Sprintf("cannot force reset the validators statistics cache, error: %s", err.Error()))
		return
	}

	shared.RespondWith(c, http.StatusOK, gin.H{}, "", data.ReturnCodeSuccess)
}
