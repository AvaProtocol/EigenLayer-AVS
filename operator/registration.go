package operator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	regcoord "github.com/Layr-Labs/eigensdk-go/contracts/bindings/RegistryCoordinator"
	eigenSdkTypes "github.com/Layr-Labs/eigensdk-go/types"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)

// Entrypoint function for cmd to initialize the registration process
func RegisterToAVS(configPath string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	if err != nil {
		panic(fmt.Errorf("error creating operator from config: %w", err))
	}

	err = operator.RegisterOperatorWithAvs()
	if err != nil {
		panic(fmt.Errorf("failed to register operator with AVS: %w", err))
	}
}

func DeregisterFromAVS(configPath string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	if err != nil {
		panic(fmt.Errorf("error creator operator from config: %w", err))
	}

	if err := operator.DeregisterOperatorFromAvs(); err != nil {
		panic(fmt.Errorf("failed to deregister operator from AVS: %w", err))
	}
}

func Status(configPath string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	if err != nil {
		panic(fmt.Errorf("error creator operator from config: %w", err))
	}

	if err := operator.ReportOperatorStatus(); err != nil {
		panic(fmt.Errorf("failed to report operator status: %w", err))
	}
}

// Registration specific functions
func (o *Operator) RegisterOperatorWithAvs() error {
	// hardcode these things for now
	quorumNumbers := eigenSdkTypes.QuorumNums{eigenSdkTypes.QuorumNum(0)}
	socket := "Not Needed"
	operatorToAvsRegistrationSigSalt := [32]byte{}
	if _, err := rand.Read(operatorToAvsRegistrationSigSalt[:]); err != nil {
		return err
	}

	curBlockNum, err := o.ethClient.BlockNumber(context.Background())
	o.logger.Infof("fetch latest block num", "currentBlockNum", curBlockNum)
	if err != nil {
		o.logger.Errorf("Unable to get current block number")
		return err
	}
	o.logger.Infof("fetch latest block num", "currentBlockNum", curBlockNum)
	_, err = o.avsWriter.RegisterOperator(
		context.Background(),
		o.operatorEcdsaPrivateKey, o.blsKeypair, quorumNumbers, socket, true,
	)
	if err != nil {
		o.logger.Errorf("Unable to register operator with avs registry coordinator", err)
		return err
	}
	o.logger.Infof("Registered operator with avs registry coordinator.")

	return nil
}

type OperatorStatus struct {
	EcdsaAddress string
	// pubkey compendium related
	PubkeysRegistered bool
	G1Pubkey          string
	G2Pubkey          string
	// avs related
	RegisteredWithAvs bool
	OperatorId        string
}

// Deregistration specific functions
func (o *Operator) DeregisterOperatorFromAvs() error {
	// hardcode these things for now
	quorumNumber := eigenSdkTypes.QuorumNums{eigenSdkTypes.QuorumNum(0)}
	operatorAddr := o.operatorAddr
	o.logger.Info(
		"DeregisterOperatorFromAvs",
		"quorumNumbers", quorumNumber,
		"operatorAddr", operatorAddr,
	)

	_, err := o.avsWriter.DeregisterOperator(
		context.Background(),
		quorumNumber,
		regcoord.BN254G1Point{},
		true,
	)
	if err != nil {
		o.logger.Error("Unable to deregister operator with avs registry coordinator", err)
		return err
	}
	o.logger.Infof("Deregister operator with avs registry coordinator.")

	return nil
}

func (o *Operator) ReportOperatorStatus() error {
	o.logger.Info("checking operator status")
	operatorId, err := o.avsReader.GetOperatorId(&bind.CallOpts{}, o.operatorAddr)
	if err != nil {
		return err
	}
	pubkeysRegistered := operatorId != [32]byte{}
	registeredWithAvs := o.operatorId != [32]byte{}
	operatorStatus := OperatorStatus{
		EcdsaAddress:      o.operatorAddr.String(),
		PubkeysRegistered: pubkeysRegistered,
		G1Pubkey:          o.blsKeypair.GetPubKeyG1().String(),
		G2Pubkey:          o.blsKeypair.GetPubKeyG2().String(),
		RegisteredWithAvs: registeredWithAvs,
		OperatorId:        hex.EncodeToString(o.operatorId[:]),
	}
	operatorStatusJson, err := json.MarshalIndent(operatorStatus, "", " ")
	if err != nil {
		return err
	}
	fmt.Println(string(operatorStatusJson))
	return nil
}
