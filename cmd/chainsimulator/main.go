package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/core/check"
	"github.com/multiversx/mx-chain-core-go/core/closing"
	nodeConfig "github.com/multiversx/mx-chain-go/config"
	"github.com/multiversx/mx-chain-go/config/overridableConfig"
	"github.com/multiversx/mx-chain-go/node/chainSimulator"
	"github.com/multiversx/mx-chain-go/node/chainSimulator/components/api"
	logger "github.com/multiversx/mx-chain-logger-go"
	"github.com/multiversx/mx-chain-logger-go/file"
	"github.com/multiversx/mx-chain-simulator-go/config"
	"github.com/multiversx/mx-chain-simulator-go/pkg/facade"
	"github.com/multiversx/mx-chain-simulator-go/pkg/factory"
	endpoints "github.com/multiversx/mx-chain-simulator-go/pkg/proxy/api"
	"github.com/multiversx/mx-chain-simulator-go/pkg/proxy/configs"
	"github.com/multiversx/mx-chain-simulator-go/pkg/proxy/configs/git"
	"github.com/multiversx/mx-chain-simulator-go/pkg/proxy/creator"
	"github.com/urfave/cli"
)

const timeToAllowProxyToStart = time.Millisecond * 10
const overrideConfigFilesSeparator = ","
const tempDirPattern = "mx-chainsimulator-*"

var (
	log          = logger.GetOrCreate("chainsimulator")
	helpTemplate = `NAME:
   {{.Name}} - {{.Usage}}
USAGE:
   {{.HelpName}} {{if .VisibleFlags}}[global options]{{end}}
   {{if len .Authors}}
AUTHOR:
   {{range .Authors}}{{ . }}{{end}}
   {{end}}{{if .Commands}}
GLOBAL OPTIONS:
   {{range .VisibleFlags}}{{.}}
   {{end}}
VERSION:
   {{.Version}}
   {{end}}
`
)

func main() {
	app := cli.NewApp()
	cli.AppHelpTemplate = helpTemplate
	app.Name = "Chain Simulator"
	app.Usage = ""
	app.Flags = []cli.Flag{
		configurationFile,
		nodeOverrideConfigurationFile,
		logLevel,
		logSaveFile,
		disableAnsiColor,
		pathToNodeConfigs,
		pathToProxyConfigs,
		startTime,
		roundsPerEpoch,
		numOfShards,
		serverPort,
		roundDurationInMs,
		bypassTransactionsSignature,
		numValidatorsPerShard,
		numWaitingValidatorsPerShard,
		numValidatorsMeta,
		numWaitingValidatorsMeta,
		initialRound,
		initialNonce,
		initialEpoch,
		autoGenerateBlocks,
		blockTimeInMs,
		skipConfigsDownload,
		fetchConfigsAndClose,
		pathWhereToSaveLogs,
		restApiInterface,
		unsafeAllowPublicBind,
	}

	app.Authors = []cli.Author{
		{
			Name:  "The MultiversX Team",
			Email: "contact@multiversx.com",
		},
	}

	app.Action = startChainSimulator

	err := app.Run(os.Args)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func startChainSimulator(ctx *cli.Context) error {
	cfg, err := loadMainConfig(ctx.GlobalString(configurationFile.Name))
	if err != nil {
		return fmt.Errorf("%w while loading the config file", err)
	}

	overrideConfigsHandler := config.NewOverrideConfigsHandler()
	overrideFiles := determineOverrideConfigFiles(ctx)
	log.Info("using the override config files", "files", overrideFiles)
	overrideCfg, err := overrideConfigsHandler.ReadAll(overrideFiles...)
	if err != nil {
		return fmt.Errorf("%w while loading the node override config files", err)
	}

	applyFlags(ctx, &cfg)

	fileLogging, err := initializeLogger(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%w while initializing the logger", err)
	}

	skipDownload := ctx.GlobalBool(skipConfigsDownload.Name)
	nodeConfigs := ctx.GlobalString(pathToNodeConfigs.Name)
	proxyConfigs := ctx.GlobalString(pathToProxyConfigs.Name)
	fetchConfigsAndCloseBool := ctx.GlobalBool(fetchConfigsAndClose.Name)
	err = fetchConfigs(skipDownload, cfg, nodeConfigs, proxyConfigs)
	if err != nil {
		return fmt.Errorf("%w while fetching configs", err)
	}
	if fetchConfigsAndCloseBool {
		return nil
	}

	bypassTxsSignature := ctx.GlobalBool(bypassTransactionsSignature.Name)
	log.Warn("signature", "bypass", bypassTxsSignature)
	roundDurationInMillis := uint64(cfg.Config.Simulator.RoundDurationInMs)
	rounds := core.OptionalUint64{
		HasValue: true,
		Value:    uint64(cfg.Config.Simulator.RoundsPerEpoch),
	}

	numValidatorsShard := ctx.GlobalInt(numValidatorsPerShard.Name)
	if numValidatorsShard < 1 {
		return errors.New("invalid value for the number of validators per shard")
	}
	numWaitingValidatorsShard := ctx.GlobalInt(numWaitingValidatorsPerShard.Name)
	if numWaitingValidatorsShard < 0 {
		return errors.New("invalid value for the number of waiting validators per shard")
	}

	numValidatorsMetaShard := ctx.GlobalInt(numValidatorsMeta.Name)
	if numValidatorsMetaShard < 1 {
		return errors.New("invalid value for the number of validators for metachain")
	}
	numWaitingValidatorsMetaShard := ctx.GlobalInt(numWaitingValidatorsMeta.Name)
	if numWaitingValidatorsMetaShard < 0 {
		return errors.New("invalid value for the number of waiting validators for metachain")
	}

	// ISSUE-004: read the bind host from a CLI flag (default "localhost")
	// and refuse non-loopback values unless the operator passed
	// --unsafe-allow-public-bind. The simulator has NO authentication on
	// its mutating endpoints; a non-loopback bind would expose
	// generate-blocks / set-state / add-keys / force-epoch-change to the
	// network. The check below catches that misconfiguration at startup.
	localRestApiInterface := ctx.GlobalString(restApiInterface.Name)
	allowPublicBind := ctx.GlobalBool(unsafeAllowPublicBind.Name)
	if !isLoopbackBindHost(localRestApiInterface) {
		if !allowPublicBind {
			return fmt.Errorf(
				"refusing to bind simulator REST API to non-loopback host %q without --unsafe-allow-public-bind; "+
					"the simulator has no authentication and exposes state-mutating endpoints",
				localRestApiInterface,
			)
		}
		// ISSUE-004 layer 2: when public bind is explicitly authorized,
		// require the auth token env var to be set. Public-bind +
		// no-token is the dangerous combination the bind-safety check
		// alone cannot prevent (the operator already opted into public
		// bind), and the auth middleware would silently no-op without
		// the env var. Refuse to start; force the operator to set
		// MX_CHAIN_SIMULATOR_AUTH_TOKEN before exposing the API.
		if !endpoints.IsSimulatorAuthEnabled() {
			return fmt.Errorf(
				"refusing to bind simulator REST API to non-loopback host %q without an auth token; "+
					"set the %s environment variable to a long random secret before exposing the API "+
					"(--unsafe-allow-public-bind acknowledges public exposure but does not waive auth)",
				localRestApiInterface, endpoints.SimulatorAuthTokenEnv,
			)
		}
		log.Warn("simulator REST API bound to a non-loopback host; auth token enforced on mutating endpoints",
			"host", localRestApiInterface,
			"auth_env_var", endpoints.SimulatorAuthTokenEnv)
	} else if endpoints.IsSimulatorAuthEnabled() {
		log.Info("simulator REST API auth token configured; mutating endpoints require Authorization: Bearer <token>")
	}
	apiConfigurator := api.NewFreePortAPIConfigurator(localRestApiInterface)
	startTimeUnix := ctx.GlobalInt64(startTime.Name)

	tempDir, err := os.MkdirTemp(os.TempDir(), tempDirPattern)
	if err != nil {
		return err
	}

	var alterConfigsError error
	argsChainSimulator := chainSimulator.ArgsChainSimulator{
		BypassTxSignatureCheck:   bypassTxsSignature,
		TempDir:                  tempDir,
		PathToInitialConfig:      nodeConfigs,
		NumOfShards:              uint32(cfg.Config.Simulator.NumOfShards),
		GenesisTimestamp:         startTimeUnix,
		RoundDurationInMillis:    roundDurationInMillis,
		RoundsPerEpoch:           rounds,
		ApiInterface:             apiConfigurator,
		MinNodesPerShard:         uint32(numValidatorsShard),
		NumNodesWaitingListShard: uint32(numWaitingValidatorsShard),
		MetaChainMinNodes:        uint32(numValidatorsMetaShard),
		NumNodesWaitingListMeta:  uint32(numWaitingValidatorsMetaShard),
		InitialRound:             cfg.Config.Simulator.InitialRound,
		InitialNonce:             cfg.Config.Simulator.InitialNonce,
		InitialEpoch:             cfg.Config.Simulator.InitialEpoch,
		AlterConfigsFunction: func(cfg *nodeConfig.Configs) {
			alterConfigsError = overridableConfig.OverrideConfigValues(overrideCfg.OverridableConfigTomlValues, cfg)
		},
		VmQueryDelayAfterStartInMs: 0,
	}
	simulator, err := chainSimulator.NewChainSimulator(argsChainSimulator)
	if err != nil {
		return err
	}

	if alterConfigsError != nil {
		return alterConfigsError
	}

	log.Info("simulators were initialized")

	for shardID := uint32(0); shardID < uint32(cfg.Config.Simulator.NumOfShards); shardID++ {
		err = simulator.GetNodeHandler(shardID).SetKeyValueForAddress(core.SystemAccountAddress, make(map[string]string))
		if err != nil {
			return err
		}
	}

	err = simulator.GenerateBlocks(1)
	if err != nil {
		return err
	}

	generator, err := factory.CreateBlocksGenerator(simulator, cfg.Config.BlocksGenerator)
	if err != nil {
		return err
	}

	metaNode := simulator.GetNodeHandler(core.MetachainShardId)
	restApiInterfaces := simulator.GetRestAPIInterfaces()
	outputProxyConfigs, err := configs.CreateProxyConfigs(configs.ArgsProxyConfigs{
		TemDir:            tempDir,
		PathToProxyConfig: proxyConfigs,
		RestApiInterfaces: restApiInterfaces,
		InitialWallets:    simulator.GetInitialWalletKeys().BalanceWallets,
	})
	if err != nil {
		return err
	}

	proxyPort := cfg.Config.Simulator.ServerPort
	proxyURL := fmt.Sprintf("%s:%d", localRestApiInterface, proxyPort)
	if proxyPort == 0 {
		proxyURL = apiConfigurator.RestApiInterface(0)
		portString := proxyURL[len(localRestApiInterface)+1:]
		port, errConvert := strconv.Atoi(portString)
		if errConvert != nil {
			return fmt.Errorf("internal error while searching a free port for the proxy component: %w", errConvert)
		}
		proxyPort = port
	}

	outputProxyConfigs.Config.GeneralSettings.ServerPort = proxyPort
	outputProxy, err := creator.CreateProxy(creator.ArgsProxy{
		Config:         outputProxyConfigs.Config,
		NodeHandler:    metaNode,
		PathToConfig:   outputProxyConfigs.PathToTempConfig,
		PathToPemFile:  outputProxyConfigs.PathToPemFile,
		NumberOfShards: uint32(cfg.Config.Simulator.NumOfShards),
	})
	if err != nil {
		return err
	}

	proxyInstance := outputProxy.ProxyHandler

	simulatorFacade, err := facade.NewSimulatorFacade(simulator, outputProxy.ProxyTransactionHandler)
	if err != nil {
		return err
	}

	endpointsProc, err := endpoints.NewEndpointsProcessor(simulatorFacade)
	if err != nil {
		return err
	}

	err = endpointsProc.ExtendProxyServer(proxyInstance.GetHttpServer())
	if err != nil {
		return err
	}

	proxyInstance.Start()

	time.Sleep(timeToAllowProxyToStart)
	log.Info(fmt.Sprintf("chain simulator's is accessible through the URL %s", proxyURL))

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-interrupt

	log.Info("close")

	generator.Close()

	simulator.Close()
	proxyInstance.Close()

	if !check.IfNilReflect(fileLogging) {
		err = fileLogging.Close()
		log.LogIfError(err)
	}

	return nil
}

func initializeLogger(ctx *cli.Context, cfg config.Config) (closing.Closer, error) {
	logLevelFlagValue := ctx.GlobalString(logLevel.Name)
	err := logger.SetLogLevel(logLevelFlagValue)
	if err != nil {
		return nil, err
	}

	withLogFile := ctx.GlobalBool(logSaveFile.Name)
	if !withLogFile {
		return nil, nil
	}

	pathLogsSave := ctx.GlobalString(pathWhereToSaveLogs.Name)
	fileLogging, err := file.NewFileLogging(file.ArgsFileLogging{
		WorkingDir:      pathLogsSave,
		DefaultLogsPath: cfg.Config.Logs.LogsPath,
		LogFilePrefix:   cfg.Config.Logs.LogFilePrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("%w creating a log file", err)
	}

	err = fileLogging.ChangeFileLifeSpan(
		time.Second*time.Duration(cfg.Config.Logs.LogFileLifeSpanInSec),
		uint64(cfg.Config.Logs.LogFileLifeSpanInMB),
	)
	if err != nil {
		return nil, err
	}

	disableAnsi := ctx.GlobalBool(disableAnsiColor.Name)
	err = removeANSIColorsForLoggerIfNeeded(disableAnsi)
	if err != nil {
		return nil, err
	}

	return fileLogging, nil
}

func fetchConfigs(skipDownload bool, cfg config.Config, nodeConfigs, proxyConfigs string) error {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return errors.New("cannot read build info")
	}
	if skipDownload {
		log.Warn(`flag "skip-configs-download" has been provided, if the configs are missing, then simulator will not start`)
		return nil
	}

	gitFetcher := git.NewGitFetcher()
	configsFetcher, err := configs.NewConfigsFetcher(cfg.Config.Simulator.MxChainRepo, cfg.Config.Simulator.MxProxyRepo, gitFetcher)
	if err != nil {
		return err
	}

	err = configsFetcher.FetchNodeConfigs(buildInfo, nodeConfigs)
	if err != nil {
		return err
	}

	return configsFetcher.FetchProxyConfigs(buildInfo, proxyConfigs)
}

func loadMainConfig(filepath string) (config.Config, error) {
	cfg := config.Config{}
	err := core.LoadTomlFile(&cfg, filepath)

	return cfg, err
}

// isLoopbackBindHost reports whether the given hostname binds the
// simulator REST API to a loopback address only. "localhost", "127.0.0.1",
// "::1" and any other IP whose .IsLoopback() returns true qualify. An
// empty hostname is treated as NOT loopback because gin's
// `engine.Run("")` would bind to all interfaces. See issues/ISSUE-004.
func isLoopbackBindHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Non-empty hostname that isn't "localhost" and doesn't parse as
		// an IP — assume non-loopback. Operators who really want to
		// custom-bind to a hostname that resolves to loopback should
		// pass --unsafe-allow-public-bind and accept the warning.
		return false
	}
	return ip.IsLoopback()
}

func determineOverrideConfigFiles(ctx *cli.Context) []string {
	overrideFiles := strings.Split(ctx.GlobalString(nodeOverrideConfigurationFile.Name), overrideConfigFilesSeparator)

	for _, filename := range overrideFiles {
		if strings.Contains(filename, nodeOverrideDefaultFilename) {
			return overrideFiles
		}
	}

	return append([]string{nodeOverrideDefaultPath}, overrideFiles...)
}

func removeANSIColorsForLoggerIfNeeded(disableAnsi bool) error {
	if !disableAnsi {
		return nil
	}

	err := logger.RemoveLogObserver(os.Stdout)
	if err != nil {
		return err
	}

	return logger.AddLogObserver(os.Stdout, &logger.PlainFormatter{})
}
