package operator

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"

	eigensdkecdsa "github.com/Layr-Labs/eigensdk-go/crypto/ecdsa"
)

type CreateAliasKeyOption struct {
	Filename   string // full path to the ecdsa json key file
	Passphrase string
	PrivateKey string
}

// Create or import
func CreateOrImportAliasKey(o CreateAliasKeyOption) {
	var err error
	var aliasEcdsaPair *ecdsa.PrivateKey

	if len(o.Passphrase) == 0 {
		fmt.Printf("missing pass phrase. aborted\n")
		return
	}

	if len(o.Passphrase) <= 12 {
		fmt.Printf("Pass pharease is too short, it should have at least 12 character. aborted\n")
		return
	}

	aliasEcdsaPair, err = eigensdkecdsa.ReadKey(o.Filename, o.Passphrase)
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

	if err = eigensdkecdsa.WriteKey(o.Filename, aliasEcdsaPair, o.Passphrase); err != nil {
		fmt.Printf("Error writing the file %s: %v\n", o.Filename, err)
		return
	}

	fmt.Printf("alias key is succesfully written to %s and encrypted with your provider passphrease.\n", o.Filename)
}

// Declare alias key for the operator
func DeclareAliasKey(configPath, address string) {
	operator, err := NewOperatorFromConfigFile(configPath)
	if err != nil {
		fmt.Errorf("error creator operator from config: %w", err)
	}

	err = operator.DeclareAlias(address)
}

func (o *Operator) DeclareAlias(address string) {
}
