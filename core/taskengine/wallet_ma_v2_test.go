package taskengine

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

const mav2TestChain = int64(11155111)

func mav2Owner() common.Address {
	return common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
}

// The factory address is what keeps MA v2 and v0.6 records from colliding in
// the wsalt: index. If both used the same factory, registering an MA v2 wallet
// at salt 0 would overwrite the v0.6 index entry for the same owner and salt,
// silently repointing an existing user's canonical wallet.
func TestMAv2AndV06IndexKeysDoNotCollide(t *testing.T) {
	owner := mav2Owner()
	salt := big.NewInt(0)
	mav2Key := WalletBySaltKey(mav2TestChain, owner, MAv2WalletFactory(), salt)
	v06Key := WalletBySaltKey(mav2TestChain, owner, common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"), salt)

	if string(mav2Key) == string(v06Key) {
		t.Fatal("MA v2 and v0.6 index keys are identical; registering one would clobber the other")
	}
	if !strings.Contains(strings.ToLower(string(mav2Key)), strings.ToLower(MAv2WalletFactory().Hex()[2:])) {
		t.Errorf("index key %q does not carry the factory address", mav2Key)
	}
}

// The whole point of registration: the sponsorship webhook resolves a sender
// back to an owner by scanning `w:<chain>:` and refuses anything it cannot
// find. A record written under the MA v2 factory has to be discoverable by
// that scan, or every sponsored operation from the wallet is denied as an
// unknown sender.
func TestMAv2RecordIsDiscoverableBySenderScan(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	owner := mav2Owner()
	addr := common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0")
	factory := MAv2WalletFactory()
	wallet := &model.SmartWallet{Owner: &owner, Address: &addr, Factory: &factory, Salt: big.NewInt(0)}

	if err := StoreWallet(db, mav2TestChain, owner, wallet); err != nil {
		t.Fatalf("StoreWallet: %v", err)
	}

	// Mirror the webhook's lookup: scan the chain prefix, match on the
	// trailing wallet segment, read the owner out of the key.
	prefix := []byte(fmt.Sprintf("w:%d:", mav2TestChain))
	want := strings.ToLower(addr.Hex())
	var foundOwner string
	err := db.IterateKeysOnly(prefix, func(key []byte) error {
		parts := strings.Split(string(key), ":")
		if len(parts) == 4 && parts[3] == want {
			foundOwner = parts[2]
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if foundOwner == "" {
		t.Fatal("MA v2 wallet not found by the sender scan; sponsorship would deny it as an unknown sender")
	}
	if !strings.EqualFold(foundOwner, owner.Hex()) {
		t.Errorf("scan resolved owner %s, want %s", foundOwner, owner.Hex())
	}
}

// Guards the assumption the whole file rests on: the factory constant here is
// the same one aa derives against. If they drift, wallets get recorded under a
// factory that did not create them.
func TestWalletFactoryMatchesDerivationFactory(t *testing.T) {
	if MAv2WalletFactory() != aa.MAv2FactoryAddress() {
		t.Errorf("taskengine factory %s != aa factory %s",
			MAv2WalletFactory().Hex(), aa.MAv2FactoryAddress().Hex())
	}
}
