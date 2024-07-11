package operator

import (
	"fmt"
	"os"
)

// lookup and return passphrase from env var. panic to fail fast if a passphrase
// isn't existed in the env
func loadECDSAPassword() string {
	passphrase, ok := os.LookupEnv("OPERATOR_ECDSA_KEY_PASSWORD")
	if !ok {
		panic(fmt.Errorf("missing OPERATOR_ECDSA_KEY_PASSWORD env var"))
	}

	if passphrase == "" {
		panic("passphrase is empty. pleae make sure you define OPERATOR_ECDSA_KEY_PASSWORD")
	}

	return passphrase
}
