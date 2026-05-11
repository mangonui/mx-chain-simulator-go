package facade

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/core/check"
	"github.com/multiversx/mx-chain-core-go/data/transaction"
	"github.com/multiversx/mx-chain-go/node/chainSimulator/dtos"
	logger "github.com/multiversx/mx-chain-logger-go"
	dtoc "github.com/multiversx/mx-chain-simulator-go/pkg/dtos"
)

const (
	errMsgTargetEpochLowerThanCurrentEpoch = "target epoch must be greater than current epoch"
	errMsgAccountNotFound                  = "account was not found"
	maxValidatorKeys                       = 400
	maxEpochDelta                          = uint32(100)
)

var log = logger.GetOrCreate("simulator/facade")

var errPendingTransaction = errors.New("something went wrong, transaction is still in pending")

type simulatorFacade struct {
	simulator          SimulatorHandler
	transactionHandler ProxyTransactionsHandler

	// mutMutating serialises every endpoint that mutates the underlying
	// simulator (block generation, state writes, validator-key
	// management, forced epoch transitions). The simulator handler's
	// own thread-safety is not documented as guaranteed; concurrent
	// HTTP requests to two of these endpoints would otherwise race on
	// the same handler. Read-only endpoints (queries, status) remain
	// unsynchronised.
	mutMutating sync.Mutex
}

// NewSimulatorFacade will create a new instance of simulatorFacade
func NewSimulatorFacade(simulator SimulatorHandler, transactionHandler ProxyTransactionsHandler) (*simulatorFacade, error) {
	if check.IfNil(simulator) {
		return nil, errNilSimulatorHandler
	}
	if check.IfNilReflect(transactionHandler) {
		return nil, errNilProxyTransactionsHandler
	}

	return &simulatorFacade{
		simulator:          simulator,
		transactionHandler: transactionHandler,
	}, nil
}

// GenerateBlocks will generate a provided number of blocks
func (sf *simulatorFacade) GenerateBlocks(numOfBlocks int) error {
	if numOfBlocks <= 0 {
		return errInvalidNumOfBlocks
	}
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()
	return sf.simulator.GenerateBlocks(numOfBlocks)
}

// GetInitialWalletKeys will return the initial wallets
func (sf *simulatorFacade) GetInitialWalletKeys() *dtos.InitialWalletKeys {
	return sf.simulator.GetInitialWalletKeys()
}

// SetKeyValueForAddress will set the provided state for an address
func (sf *simulatorFacade) SetKeyValueForAddress(address string, keyValueMap map[string]string) error {
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()
	return sf.simulator.SetKeyValueForAddress(address, keyValueMap)
}

// SetStateMultiple will set the entire state for the provided addresses
func (sf *simulatorFacade) SetStateMultiple(stateSlice []*dtos.AddressState, noGenerate bool) error {
	if len(stateSlice) > 1024 {
		return errors.New("too many state entries")
	}
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()

	err := sf.simulator.SetStateMultiple(stateSlice)
	if err != nil {
		return err
	}

	if noGenerate {
		return nil
	}

	return sf.simulator.GenerateBlocks(1)
}

// SetStateMultipleOverwrite will set the entire state for the provided address and cleanup the old state of the provided addresses
func (sf *simulatorFacade) SetStateMultipleOverwrite(stateSlice []*dtos.AddressState, noGenerate bool) error {
	if len(stateSlice) > 1024 {
		return errors.New("too many state entries")
	}
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()

	for _, state := range stateSlice {
		// TODO MX-15414
		err := sf.simulator.RemoveAccounts([]string{state.Address})
		shouldReturnErr := err != nil && !strings.Contains(err.Error(), errMsgAccountNotFound)
		if shouldReturnErr {
			return err
		}
	}

	err := sf.simulator.SetStateMultiple(stateSlice)
	if err != nil {
		return err
	}

	if noGenerate {
		return nil
	}

	return sf.simulator.GenerateBlocks(1)
}

// AddValidatorKeys will add the validator keys in the multi key handler
func (sf *simulatorFacade) AddValidatorKeys(validators *dtoc.ValidatorKeys) error {
	if validators == nil || len(validators.PrivateKeysBase64) > maxValidatorKeys {
		return errors.New("invalid validator keys count")
	}
	validatorsPrivateKeys := make([][]byte, 0, len(validators.PrivateKeysBase64))
	for idx, privateKeyBase64 := range validators.PrivateKeysBase64 {
		privateKeyHexBytes, err := base64.StdEncoding.DecodeString(privateKeyBase64)
		if err != nil {
			return fmt.Errorf("cannot base64 decode key index=%d, error=%s", idx, err.Error())
		}

		privateKeyBytes, err := hex.DecodeString(string(privateKeyHexBytes))
		if err != nil {
			return fmt.Errorf("cannot hex decode key index=%d, error=%s", idx, err.Error())
		}

		validatorsPrivateKeys = append(validatorsPrivateKeys, privateKeyBytes)
	}

	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()
	return sf.simulator.AddValidatorKeys(validatorsPrivateKeys)
}

// GenerateBlocksUntilEpochIsReached will generate as many blocks are required until the target epoch is reached
func (sf *simulatorFacade) GenerateBlocksUntilEpochIsReached(targetEpoch int32) error {
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()
	return sf.simulator.GenerateBlocksUntilEpochIsReached(targetEpoch)
}

// ForceUpdateValidatorStatistics will force the reset of the cache used for the validators statistics endpoint
func (sf *simulatorFacade) ForceUpdateValidatorStatistics() error {
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()
	return sf.simulator.ForceResetValidatorStatisticsCache()
}

// ForceChangeOfEpoch will force change the current epoch
func (sf *simulatorFacade) ForceChangeOfEpoch(targetEpoch uint32) error {
	sf.mutMutating.Lock()
	defer sf.mutMutating.Unlock()

	if targetEpoch == 0 {
		return sf.simulator.ForceChangeOfEpoch()
	}

	currentEpoch, err := sf.getCurrentEpoch()
	if err != nil {
		return err
	}
	if currentEpoch >= targetEpoch {
		return fmt.Errorf("%s, current epoch: %d target epoch: %d", errMsgTargetEpochLowerThanCurrentEpoch, currentEpoch, targetEpoch)
	}
	if targetEpoch-currentEpoch > maxEpochDelta {
		return fmt.Errorf("target epoch delta exceeds maximum: %d", maxEpochDelta)
	}

	for currentEpoch < targetEpoch {
		err := sf.simulator.ForceChangeOfEpoch()
		if err != nil {
			return err
		}

		currentEpoch, err = sf.getCurrentEpoch()
		if err != nil {
			return err
		}
	}

	return nil
}

// GetObserversInfo will return information about the observers
func (sf *simulatorFacade) GetObserversInfo() (map[uint32]*dtoc.ObserverInfo, error) {
	restApiInterface := sf.simulator.GetRestAPIInterfaces()

	response := make(map[uint32]*dtoc.ObserverInfo)
	for shardID, apiInterface := range restApiInterface {
		split := strings.Split(apiInterface, ":")
		if len(split) != 2 {
			return nil, fmt.Errorf("cannot extract port for shard ID=%d", shardID)
		}

		port, err := strconv.Atoi(split[1])
		if err != nil {
			return nil, fmt.Errorf("cannot cast port string to int for shard ID=%d", shardID)
		}

		response[shardID] = &dtoc.ObserverInfo{
			APIPort: port,
		}
	}

	return response, nil
}

// GenerateBlocksUntilTransactionIsProcessed generate blocks until the status of the provided transaction hash is processed
func (sf *simulatorFacade) GenerateBlocksUntilTransactionIsProcessed(txHash string, maxNumOfBlocksToGenerate int) error {
	log.Debug("GenerateBlocksUntilTransactionIsProcessed", "tx hash", txHash, "maxNumOfBlocksToGenerate", maxNumOfBlocksToGenerate)
	for i := 0; i < maxNumOfBlocksToGenerate; i++ {
		txStatusInfo, err := sf.transactionHandler.GetProcessedTransactionStatus(txHash)
		if err != nil {
			return err
		}

		if txStatusInfo.Status != transaction.TxStatusPending.String() {
			return nil
		}

		err = sf.GenerateBlocks(1)
		if err != nil {
			return err
		}
	}

	return errors.New("something went wrong, transaction is still in pending")
}

func (sf *simulatorFacade) getCurrentEpoch() (uint32, error) {
	nodeHandler := sf.simulator.GetNodeHandler(core.MetachainShardId)
	if check.IfNil(nodeHandler) {
		return 0, errors.New("missing metachain node handler")
	}
	processComponents := nodeHandler.GetProcessComponents()
	if check.IfNil(processComponents) {
		return 0, errors.New("missing process components")
	}
	epochStartTrigger := processComponents.EpochStartTrigger()
	if check.IfNil(epochStartTrigger) {
		return 0, errors.New("missing epoch start trigger")
	}

	return epochStartTrigger.Epoch(), nil
}

// IsInterfaceNil returns true if there is no value under the interface
func (sf *simulatorFacade) IsInterfaceNil() bool {
	return sf == nil
}
