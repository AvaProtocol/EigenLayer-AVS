package operator

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/apconfig"
	"github.com/ethereum/go-ethereum/crypto"

	eigensdkecdsa "github.com/Layr-Labs/eigensdk-go/crypto/ecdsa"
)

type CreateAliasKeyOption struct {
	Filename   string // full path to the ecdsa json key file
	PrivateKey string
}

// Create or import
func CreateOrImportAliasKey(o CreateAliasKeyOption) {
	var err error
	var aliasEcdsaPair *ecdsa.PrivateKey

	passphrase := loadECDSAPassword()

	if len(passphrase) == 0 {
		fmt.Printf("missing pass phrase. aborted\n")
		return
	}

	if len(passphrase) <= 12 {
		fmt.Printf("Pass pharease is too short, it should have at least 12 character. aborted\n")
		return
	}

	aliasEcdsaPair, err = eigensdkecdsa.ReadKey(o.Filename, passphrase)
	if err == nil {
		fmt.Printf("%s key already existed. we won't override the key.\nTo write to a different file, set the `--name` parameter.\nUse `--help` to view parameter detail.\n", o.Filename)
		return
	}

	if o.PrivateKey == "" {
		aliasEcdsaPair, err = crypto.GenerateKey()
		if err != nil {
			panic(fmt.Errorf("cannot generate key %w", err))
		}
	} else {
		aliasEcdsaPair, err = crypto.HexToECDSA(o.PrivateKey)
		if err != nil {
			panic(fmt.Errorf("cannot import provided private key %s with error: %w", o.PrivateKey, err))
		}
	}

	if err = eigensdkecdsa.WriteKey(o.Filename, aliasEcdsaPair, passphrase); err != nil {
		fmt.Printf("Error writing the file %s: %v\n", o.Filename, err)
		return
	}

	fmt.Printf("alias key is successfully written to %s and encrypted with your provider passphrease.\n", o.Filename)
}

// Declare alias key for the operator
func DeclareAlias(configPath, address string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	if err != nil {
		panic(fmt.Errorf("error creator operator from config: %w", err))
	}

	if err = operator.DeclareAlias(address); err != nil {
		panic(err)
	}
}

func (o *Operator) DeclareAlias(filepath string) error {
	apConfigContract, err := apconfig.GetContract(o.config.EthRpcUrl, o.apConfigAddr)
	if err != nil {
		panic(fmt.Errorf("cannot create apconfig contract writer: %w", err))
	}

	noSendTxOpts, err := o.txManager.GetNoSendTxOpts()
	if err != nil {
		return fmt.Errorf("Error creating transaction object %v", err)
	}

	passphrase := loadECDSAPassword()

	aliasEcdsaPair, err := eigensdkecdsa.ReadKey(filepath, passphrase)
	if err != nil {
		return fmt.Errorf("cannot parse the alias ecdsa key file %v", err)
	}

	tx, err := apConfigContract.DeclareAlias(
		noSendTxOpts,
		crypto.PubkeyToAddress(aliasEcdsaPair.PublicKey),
	)
	if err != nil {
		return fmt.Errorf("Failed to create APConfig.declareAlias transaction %v", err)
	}

	ctx := context.Background()
	receipt, err := o.txManager.Send(ctx, tx, true)
	if err != nil {
		return fmt.Errorf("declareAlias transaction failed %w", err)
	}

	if receipt.Status != 1 {
		return fmt.Errorf("declareAlias transaction %s reverted", receipt.TxHash.Hex())
	}

	fmt.Printf("successfully declared an alias for operator %s alias address %s at tx %s ", o.operatorAddr.String(), crypto.PubkeyToAddress(aliasEcdsaPair.PublicKey), receipt.TxHash.Hex())
	return nil
}

// Remove alias key for the operator
func RemoveAlias(configPath string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	fmt.Println(configPath)
	if err != nil {
		panic(fmt.Errorf("error creator operator from config: %w", err))
	}

	if err = operator.RemoveAlias(); err != nil {
		panic(err)
	}
}

func (o *Operator) RemoveAlias() error {
	apConfigContract, err := apconfig.GetContract(o.config.EthRpcUrl, o.apConfigAddr)
	if err != nil {
		panic(fmt.Errorf("cannot create apconfig contract writer: %w", err))
	}

	if o.signerAddress.Cmp(o.operatorAddr) == 0 {
		return fmt.Errorf("not using alias key")
	}

	noSendTxOpts, err := o.txManager.GetNoSendTxOpts()
	if err != nil {
		return fmt.Errorf("Error creating transaction object %v", err)
	}

	tx, err := apConfigContract.Undeclare(noSendTxOpts)
	if err != nil {
		return fmt.Errorf("Failed to create APConfig.declareAlias transaction %v", err)
	}

	ctx := context.Background()
	receipt, err := o.txManager.Send(ctx, tx, true)
	if err != nil {
		return fmt.Errorf("declareAlias transaction failed %w", err)
	}

	if receipt.Status != 1 {
		return fmt.Errorf("declareAlias transaction %s reverted", receipt.TxHash.Hex())
	}

	fmt.Printf("successfully remove alias %s for operator %s  at tx %s ", o.signerAddress.String(), o.operatorAddr.String(), receipt.TxHash.Hex())
	return nil
}
