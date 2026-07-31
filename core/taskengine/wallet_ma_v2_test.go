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

func TestIsMAv2Wallet(t *testing.T) {
	mav2 := MAv2WalletFactory()
	v06 := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	owner, addr := mav2Owner(), common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0")

	tests := []struct {
		name   string
		wallet *model.SmartWallet
		want   bool
	}{
		{"MA v2 factory", &model.SmartWallet{Owner: &owner, Address: &addr, Factory: &mav2}, true},
		{"v0.6 factory", &model.SmartWallet{Owner: &owner, Address: &addr, Factory: &v06}, false},
		{"no factory recorded is v0.6 by definition", &model.SmartWallet{Owner: &owner, Address: &addr}, false},
		{"nil wallet", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMAv2Wallet(tt.wallet); got != tt.want {
				t.Errorf("IsMAv2Wallet = %v, want %v", got, tt.want)
			}
		})
	}
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

func TestGetOrCreateMAv2WalletGuards(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	owner := mav2Owner()

	tests := []struct {
		name  string
		owner common.Address
		salt  *big.Int
	}{
		{"nil salt", owner, nil},
		{"negative salt", owner, big.NewInt(-1)},
		{"zero owner", common.Address{}, big.NewInt(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// rpcConn is nil: every case here must fail on validation before
			// any RPC is attempted, so a nil client can never be dereferenced.
			if _, err := GetOrCreateMAv2Wallet(db, nil, mav2TestChain, tt.owner, tt.salt); err == nil {
				t.Error("expected an error")
			}
		})
	}

	t.Run("nil storage", func(t *testing.T) {
		if _, err := GetOrCreateMAv2Wallet(nil, nil, mav2TestChain, owner, big.NewInt(0)); err == nil {
			t.Error("expected an error for nil storage")
		}
	})
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

// A second call for the same tuple must return the existing record rather than
// writing a duplicate or resetting user-set flags.
func TestMAv2RegistrationIsIdempotent(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	owner := mav2Owner()
	addr := common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0")
	factory := MAv2WalletFactory()
	salt := big.NewInt(0)

	first := &model.SmartWallet{Owner: &owner, Address: &addr, Factory: &factory, Salt: salt, IsHidden: true}
	if err := StoreWallet(db, mav2TestChain, owner, first); err != nil {
		t.Fatalf("StoreWallet: %v", err)
	}

	// nil RPC: if the lookup path works, no derivation is attempted, so this
	// returning successfully is itself the assertion that the index was used.
	got, err := GetOrCreateMAv2Wallet(db, nil, mav2TestChain, owner, salt)
	if err != nil {
		t.Fatalf("GetOrCreateMAv2Wallet on an already-registered wallet: %v", err)
	}
	if got.Address == nil || *got.Address != addr {
		t.Errorf("address = %v, want %s", got.Address, addr.Hex())
	}
	if !got.IsHidden {
		t.Error("IsHidden was reset; re-registration must not clobber user-set flags")
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

// Idempotency is only as strong as the lookup it rests on. If a storage read
// error were treated as "not registered", the fall-through would re-derive and
// re-store with IsHidden:false — silently un-hiding a wallet the user hid. A
// closed database is the cheapest way to make every read fail.
func TestRegistrationDoesNotFallThroughOnStorageError(t *testing.T) {
	db := testutil.TestMustDB()
	owner := mav2Owner()
	addr := common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0")
	factory := MAv2WalletFactory()
	salt := big.NewInt(0)

	if err := StoreWallet(db, mav2TestChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &addr, Factory: &factory, Salt: salt, IsHidden: true,
	}); err != nil {
		t.Fatalf("StoreWallet: %v", err)
	}
	db.Close() // every subsequent read now errors rather than reporting absence

	// nil RPC: if this wrongly falls through to derive-and-store it will panic
	// or error on the RPC, either way not returning a fresh IsHidden:false
	// record. What must NOT happen is a silent success that clobbers the flag.
	got, err := GetOrCreateMAv2Wallet(db, nil, mav2TestChain, owner, salt)
	if err == nil {
		if got != nil && !got.IsHidden {
			t.Fatal("returned a re-registered record with IsHidden reset; the storage error was masked")
		}
		t.Fatal("expected an error when storage reads fail, got success")
	}
	if !strings.Contains(err.Error(), "looking up") && !strings.Contains(err.Error(), "reading registered") {
		t.Errorf("error should name the failed lookup, got: %v", err)
	}
}
