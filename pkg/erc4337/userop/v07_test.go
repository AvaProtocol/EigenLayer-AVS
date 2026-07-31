package userop

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// EntryPoint v0.7, same address on every chain.
var entryPointV07 = common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

// sepoliaChainID is the chain the expected hashes below were read from. The
// hash binds to the chain id, so these vectors are only valid for it.
var sepoliaChainID = big.NewInt(11155111)

func addr(s string) *common.Address { a := common.HexToAddress(s); return &a }

// The expected hashes are not hand-computed. Each was read from the deployed
// EntryPoint v0.7 on Sepolia via
//
//	cast call 0x0000000071727De22E5E9d8BAf0edAc6f37da032 \
//	  'getUserOpHash((address,uint256,bytes,bytes,bytes32,uint256,bytes32,bytes,bytes))(bytes32)' ...
//
// so the contract itself is the oracle. Regenerate them the same way if the
// packing ever needs to change.
func TestGetUserOpHash_MatchesDeployedEntryPoint(t *testing.T) {
	tests := []struct {
		name string
		op   UserOperationV07
		want string
	}{
		{
			name: "all zero",
			op: UserOperationV07{
				Sender:               common.HexToAddress("0x0"),
				Nonce:                big.NewInt(0),
				CallData:             []byte{},
				CallGasLimit:         big.NewInt(0),
				VerificationGasLimit: big.NewInt(0),
				PreVerificationGas:   big.NewInt(0),
				MaxFeePerGas:         big.NewInt(0),
				MaxPriorityFeePerGas: big.NewInt(0),
			},
			want: "0xc2ae9ba70cf313be10ea729190547ac0dfa49cb6501877a84bec112f30781863",
		},
		{
			name: "deployed account, unsponsored",
			op: UserOperationV07{
				Sender:               common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b"),
				Nonce:                big.NewInt(7),
				CallData:             hexutil.MustDecode("0xb61d27f6000000000000000000000000c60e71bd0f2e6d8832fea1a2d56091c48493c788"),
				VerificationGasLimit: big.NewInt(100000),
				CallGasLimit:         big.NewInt(200000),
				PreVerificationGas:   big.NewInt(21000),
				MaxPriorityFeePerGas: big.NewInt(1000000000),
				MaxFeePerGas:         big.NewInt(2000000000),
			},
			want: "0x3ba23f9f10cdf7dbf0ff7b13c995fe26151e5cdbb50ad5e8ead961d011a695a3",
		},
		{
			name: "counterfactual account with paymaster",
			op: UserOperationV07{
				Sender:                        common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0"),
				Nonce:                         big.NewInt(3),
				Factory:                       addr("0x00000000000017c61b5bEe81050EC8eFc9c6fecd"),
				FactoryData:                   hexutil.MustDecode("0xdeadbeef"),
				CallData:                      hexutil.MustDecode("0xdeadbeefcafe"),
				VerificationGasLimit:          big.NewInt(500000),
				CallGasLimit:                  big.NewInt(1000000),
				PreVerificationGas:            big.NewInt(50000),
				MaxPriorityFeePerGas:          big.NewInt(100000000),
				MaxFeePerGas:                  big.NewInt(50000000000),
				Paymaster:                     addr("0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f"),
				PaymasterVerificationGasLimit: big.NewInt(100000),
				PaymasterPostOpGasLimit:       big.NewInt(50000),
				PaymasterData:                 hexutil.MustDecode("0xc0ffee"),
			},
			want: "0x118de0f4dd62e075f2f21139965356f851435c00c5c3c24ccefd7429ada1f607",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.op.GetUserOpHash(entryPointV07, sepoliaChainID)
			if err != nil {
				t.Fatalf("GetUserOpHash: %v", err)
			}
			if got.Hex() != tt.want {
				t.Errorf("hash mismatch\n got %s\nwant %s", got.Hex(), tt.want)
			}
		})
	}
}

func TestGetUserOpHash_BindsToChainAndEntryPoint(t *testing.T) {
	op := UserOperationV07{
		Sender: common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b"),
		Nonce:  big.NewInt(7), CallData: []byte{},
		CallGasLimit: big.NewInt(1), VerificationGasLimit: big.NewInt(1),
		PreVerificationGas: big.NewInt(1), MaxFeePerGas: big.NewInt(1),
		MaxPriorityFeePerGas: big.NewInt(1),
	}
	base, err := op.GetUserOpHash(entryPointV07, sepoliaChainID)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	otherChain, err := op.GetUserOpHash(entryPointV07, big.NewInt(1))
	if err != nil {
		t.Fatalf("otherChain: %v", err)
	}
	otherEP, err := op.GetUserOpHash(common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"), sepoliaChainID)
	if err != nil {
		t.Fatalf("otherEP: %v", err)
	}
	if base == otherChain {
		t.Error("hash did not change with chain id — signatures would replay across chains")
	}
	if base == otherEP {
		t.Error("hash did not change with entrypoint — signatures would replay across entrypoints")
	}
}

func TestPackedFieldOrdering(t *testing.T) {
	op := UserOperationV07{
		VerificationGasLimit: big.NewInt(0x1111),
		CallGasLimit:         big.NewInt(0x2222),
		MaxPriorityFeePerGas: big.NewInt(0x3333),
		MaxFeePerGas:         big.NewInt(0x4444),
	}
	agl, err := op.AccountGasLimits()
	if err != nil {
		t.Fatalf("AccountGasLimits: %v", err)
	}
	// verificationGasLimit occupies the high 16 bytes, callGasLimit the low.
	if want := "0x0000000000000000000000000000111100000000000000000000000000002222"; hexutil.Encode(agl[:]) != want {
		t.Errorf("accountGasLimits\n got %s\nwant %s", hexutil.Encode(agl[:]), want)
	}
	gf, err := op.GasFees()
	if err != nil {
		t.Fatalf("GasFees: %v", err)
	}
	// maxPriorityFeePerGas is the HIGH half — the reverse of the usual reading order.
	if want := "0x0000000000000000000000000000333300000000000000000000000000004444"; hexutil.Encode(gf[:]) != want {
		t.Errorf("gasFees\n got %s\nwant %s", hexutil.Encode(gf[:]), want)
	}
}

func TestPackRejectsOversizedAndNilValues(t *testing.T) {
	tooBig := new(big.Int).Lsh(big.NewInt(1), 128) // 2^128, one past uint128

	t.Run("overflow is an error, not a truncation", func(t *testing.T) {
		op := UserOperationV07{VerificationGasLimit: tooBig, CallGasLimit: big.NewInt(1)}
		if _, err := op.AccountGasLimits(); err == nil {
			t.Fatal("expected overflow error, got nil — value would be silently truncated")
		}
	})

	t.Run("nil is an error", func(t *testing.T) {
		op := UserOperationV07{VerificationGasLimit: nil, CallGasLimit: big.NewInt(1)}
		if _, err := op.AccountGasLimits(); err == nil {
			t.Fatal("expected nil error, got nil")
		}
	})

	t.Run("hash surfaces the packing error", func(t *testing.T) {
		op := UserOperationV07{
			Sender: common.HexToAddress("0x1"), Nonce: big.NewInt(0), CallData: []byte{},
			VerificationGasLimit: tooBig, CallGasLimit: big.NewInt(1),
			PreVerificationGas: big.NewInt(0),
			MaxFeePerGas:       big.NewInt(1), MaxPriorityFeePerGas: big.NewInt(1),
		}
		if _, err := op.GetUserOpHash(entryPointV07, sepoliaChainID); err == nil {
			t.Fatal("expected error to propagate out of GetUserOpHash")
		}
	})
}

func TestInitCodeAndPaymasterAndData(t *testing.T) {
	t.Run("nil factory yields empty initCode", func(t *testing.T) {
		op := UserOperationV07{}
		if got := op.InitCode(); len(got) != 0 {
			t.Errorf("expected empty initCode, got %s", hexutil.Encode(got))
		}
	})
	t.Run("factory concatenates with data", func(t *testing.T) {
		op := UserOperationV07{
			Factory:     addr("0x00000000000017c61b5bEe81050EC8eFc9c6fecd"),
			FactoryData: hexutil.MustDecode("0xdeadbeef"),
		}
		want := "0x00000000000017c61b5bee81050ec8efc9c6fecddeadbeef"
		if got := hexutil.Encode(op.InitCode()); got != want {
			t.Errorf("initCode\n got %s\nwant %s", got, want)
		}
	})
	t.Run("nil paymaster yields empty paymasterAndData", func(t *testing.T) {
		op := UserOperationV07{}
		got, err := op.PaymasterAndData()
		if err != nil {
			t.Fatalf("PaymasterAndData: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty, got %s", hexutil.Encode(got))
		}
	})
	t.Run("paymaster packs address then two gas limits then data", func(t *testing.T) {
		op := UserOperationV07{
			Paymaster:                     addr("0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f"),
			PaymasterVerificationGasLimit: big.NewInt(100000),
			PaymasterPostOpGasLimit:       big.NewInt(50000),
			PaymasterData:                 hexutil.MustDecode("0xc0ffee"),
		}
		got, err := op.PaymasterAndData()
		if err != nil {
			t.Fatalf("PaymasterAndData: %v", err)
		}
		want := "0xf023ea291f5beda4bf59bbdc9004f1d18be19d6f" +
			"000000000000000000000000000186a0" +
			"0000000000000000000000000000c350" +
			"c0ffee"
		if hexutil.Encode(got) != want {
			t.Errorf("paymasterAndData\n got %s\nwant %s", hexutil.Encode(got), want)
		}
	})
}

// The wire shape is what the bundler actually parses, and its rules are not
// expressible in struct tags: numbers go out as hex quantity strings, and the
// factory/paymaster groups must be absent rather than present-and-empty. A
// bundler receiving `"paymaster": ""` reads it as sponsorship by the zero
// address and rejects the operation as a validation failure, which gives no
// hint that the encoding was at fault.
func TestMarshalJSON_WireShape(t *testing.T) {
	base := func() UserOperationV07 {
		return UserOperationV07{
			Sender: common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b"),
			Nonce:  big.NewInt(0), CallData: hexutil.MustDecode("0xdeadbeef"),
			CallGasLimit: big.NewInt(500000), VerificationGasLimit: big.NewInt(1500000),
			PreVerificationGas:   big.NewInt(100000),
			MaxFeePerGas:         big.NewInt(20000000000),
			MaxPriorityFeePerGas: big.NewInt(1000000000),
			Signature:            hexutil.MustDecode("0xff00"),
		}
	}
	decode := func(t *testing.T, op UserOperationV07) map[string]interface{} {
		t.Helper()
		b, err := json.Marshal(&op)
		if err != nil {
			t.Fatalf("MarshalJSON: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	t.Run("numbers are hex quantity strings, not JSON numbers", func(t *testing.T) {
		m := decode(t, base())
		for k, want := range map[string]string{
			"nonce": "0x0", "callGasLimit": "0x7a120", "verificationGasLimit": "0x16e360",
			"preVerificationGas": "0x186a0", "maxFeePerGas": "0x4a817c800",
			"maxPriorityFeePerGas": "0x3b9aca00",
		} {
			got, ok := m[k].(string)
			if !ok {
				t.Errorf("%s is %T, want a hex string", k, m[k])
				continue
			}
			if got != want {
				t.Errorf("%s = %s, want %s", k, got, want)
			}
		}
	})

	t.Run("factory and paymaster keys are absent when unused", func(t *testing.T) {
		m := decode(t, base())
		for _, k := range []string{"factory", "factoryData", "paymaster",
			"paymasterVerificationGasLimit", "paymasterPostOpGasLimit", "paymasterData"} {
			if v, present := m[k]; present {
				t.Errorf("%s present (%v); must be omitted entirely, not empty", k, v)
			}
		}
	})

	t.Run("factory group appears together", func(t *testing.T) {
		op := base()
		f := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
		op.Factory, op.FactoryData = &f, hexutil.MustDecode("0x8b4e464e")
		m := decode(t, op)
		if m["factory"] != f.Hex() {
			t.Errorf("factory = %v, want %s", m["factory"], f.Hex())
		}
		if m["factoryData"] != "0x8b4e464e" {
			t.Errorf("factoryData = %v", m["factoryData"])
		}
	})

	t.Run("paymaster group appears together", func(t *testing.T) {
		op := base()
		p := common.HexToAddress("0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f")
		op.Paymaster = &p
		op.PaymasterVerificationGasLimit = big.NewInt(100000)
		op.PaymasterPostOpGasLimit = big.NewInt(50000)
		op.PaymasterData = hexutil.MustDecode("0xc0ffee")
		m := decode(t, op)
		for k, want := range map[string]string{
			"paymaster": p.Hex(), "paymasterVerificationGasLimit": "0x186a0",
			"paymasterPostOpGasLimit": "0xc350", "paymasterData": "0xc0ffee",
		} {
			if m[k] != want {
				t.Errorf("%s = %v, want %s", k, m[k], want)
			}
		}
	})

	t.Run("half-set groups are errors, not silent drops", func(t *testing.T) {
		f := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
		p := common.HexToAddress("0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f")
		for name, mutate := range map[string]func(*UserOperationV07){
			"factoryData without factory": func(o *UserOperationV07) {
				o.FactoryData = hexutil.MustDecode("0x8b4e464e")
			},
			"factory without factoryData": func(o *UserOperationV07) { o.Factory = &f },
			"paymaster gas without paymaster": func(o *UserOperationV07) {
				o.PaymasterVerificationGasLimit = big.NewInt(1)
			},
			"paymasterData without paymaster": func(o *UserOperationV07) {
				o.PaymasterData = hexutil.MustDecode("0xc0ffee")
			},
			"paymaster without its gas limits": func(o *UserOperationV07) { o.Paymaster = &p },
		} {
			t.Run(name, func(t *testing.T) {
				op := base()
				mutate(&op)
				if _, err := json.Marshal(&op); err == nil {
					t.Error("expected an error; a half-set group would be silently dropped or malformed")
				}
			})
		}
	})

	t.Run("nil required field is an error", func(t *testing.T) {
		op := base()
		op.CallGasLimit = nil
		if _, err := json.Marshal(&op); err == nil {
			t.Error("expected an error for a nil callGasLimit")
		}
	})
}
