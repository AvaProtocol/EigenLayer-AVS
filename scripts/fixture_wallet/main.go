// Fixture-wallet setup for the live Sepolia suite: deploy a runner, fund it, or
// report what its validation entities hold.
//
//	go run ./scripts/fixture_wallet -salt 13            # report
//	go run ./scripts/fixture_wallet -salt 13 -deploy
//	go run ./scripts/fixture_wallet -salt 13 -fund 0.03
//
// Two things a live test cannot do for itself:
//
// Deploy. A wallet normally deploys via initCode on its first UserOp, but a
// fixture wants to be deployed BEFORE the test, so the test measures a grant
// replace rather than a first-deployment path. An undeployed account also
// answers every module read with zero, which makes "this entity is free" look
// true of an account that cannot tell you anything.
//
// Fund. Each live replace run costs roughly 0.0036 ETH self-funded, and a plain
// 21000-gas transfer to a smart account reverts — its receive() needs more.
package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nDEPLOY FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	saltArg := flag.String("salt", "", "fixture salt (see fixtureSalt* in session_fixture_permissions_test.go)")
	doDeploy := flag.Bool("deploy", false, "deploy the account if it has no code")
	fundEth := flag.String("fund", "", "top the runner up by this many ETH")
	flag.Parse()

	if *saltArg == "" {
		return fmt.Errorf("-salt is required")
	}
	salt, ok := new(big.Int).SetString(*saltArg, 10)
	if !ok {
		return fmt.Errorf("-salt %q is not a number", *saltArg)
	}

	cfg, err := config.NewConfig("config/test.yaml")
	if err != nil {
		return fmt.Errorf("loading config/test.yaml: %w", err)
	}
	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	if err != nil {
		return fmt.Errorf("dialing RPC: %w", err)
	}
	defer client.Close()

	keyHex := strings.TrimPrefix(strings.TrimSpace(os.Getenv("TEST_PRIVATE_KEY")), "0x")
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return fmt.Errorf("TEST_PRIVATE_KEY: %w", err)
	}
	owner := crypto.PubkeyToAddress(key.PublicKey)

	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)
	factory, err := aa.EffectiveFactory(cfg.SmartWallet)
	if err != nil {
		return fmt.Errorf("resolving effective factory: %w", err)
	}
	sender, err := aa.GetSenderAddressMAv2(client, owner, salt)
	if err != nil {
		return fmt.Errorf("deriving sender: %w", err)
	}

	ctx := context.Background()
	code, err := client.CodeAt(ctx, *sender, nil)
	if err != nil {
		return fmt.Errorf("reading code at %s: %w", sender.Hex(), err)
	}
	balance, err := client.BalanceAt(ctx, *sender, nil)
	if err != nil {
		return err
	}
	fmt.Printf("owner   %s\nfactory %s\nsalt    %s\nsender  %s\n  code    %d bytes\n  balance %s wei\n",
		owner.Hex(), factory.Hex(), salt, sender.Hex(), len(code), balance)

	if len(code) > 0 {
		if err := reportEntities(ctx, client, *sender); err != nil {
			return err
		}
	}

	if *fundEth != "" {
		if err := fund(ctx, client, key, *sender, *fundEth); err != nil {
			return err
		}
	}
	if !*doDeploy {
		if len(code) == 0 {
			fmt.Println("\nnot deployed — re-run with -deploy")
		}
		return nil
	}
	if len(code) > 0 {
		fmt.Println("\nalready deployed — nothing to do")
		return nil
	}

	deployFactory, factoryData, err := aa.DeriveInitCodeAuto(owner, factory, salt)
	if err != nil {
		return fmt.Errorf("building init code: %w", err)
	}

	chainID, err := client.ChainID(ctx)
	if err != nil {
		return err
	}
	nonce, err := client.PendingNonceAt(ctx, owner)
	if err != nil {
		return err
	}
	tip, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))

	gas, err := client.EstimateGas(ctx, ethereumCallMsg(owner, deployFactory, factoryData))
	if err != nil {
		return fmt.Errorf("estimating deploy gas: %w", err)
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, To: &deployFactory,
		Gas: gas * 12 / 10, GasFeeCap: feeCap, GasTipCap: tip, Data: factoryData,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return err
	}
	if err := client.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("sending deploy tx: %w", err)
	}
	fmt.Printf("\ndeploy tx https://sepolia.etherscan.io/tx/%s\n", signed.Hash().Hex())

	receipt, err := waitMined(ctx, client, signed.Hash())
	if err != nil {
		return err
	}
	if receipt.Status != 1 {
		return fmt.Errorf("deploy reverted in block %d", receipt.BlockNumber)
	}
	code, err = client.CodeAt(ctx, *sender, nil)
	if err != nil {
		return err
	}
	if len(code) == 0 {
		return fmt.Errorf("tx succeeded but %s still has no code — wrong factory or salt", sender.Hex())
	}
	fmt.Printf("✅ deployed %s (%d bytes of code) in block %d\n", sender.Hex(), len(code), receipt.BlockNumber)
	return nil
}

func ethereumCallMsg(from, to common.Address, data []byte) ethereum.CallMsg {
	return ethereum.CallMsg{From: from, To: &to, Data: data}
}

func waitMined(ctx context.Context, client *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	for i := 0; i < 60; i++ {
		receipt, err := client.TransactionReceipt(ctx, hash)
		if err == nil {
			return receipt, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("deploy tx %s not mined within 3 minutes", hash.Hex())
}

// fund tops the runner up from the owner EOA.
func fund(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, to common.Address, amountEth string) error {
	f, ok := new(big.Float).SetString(amountEth)
	if !ok {
		return fmt.Errorf("-fund %q is not a number", amountEth)
	}
	wei, _ := new(big.Float).Mul(f, big.NewFloat(1e18)).Int(nil)
	from := crypto.PubkeyToAddress(key.PublicKey)

	chainID, err := client.ChainID(ctx)
	if err != nil {
		return err
	}
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	tip, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, To: &to, Value: wei,
		// A smart account's receive() needs more than a plain 21000 transfer,
		// which reverts at exactly its gas limit with no useful error.
		Gas:       120000,
		GasFeeCap: new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2))),
		GasTipCap: tip,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return err
	}
	if err := client.SendTransaction(ctx, signed); err != nil {
		return err
	}
	fmt.Printf("\nfunding %s with %s ETH\nhttps://sepolia.etherscan.io/tx/%s\n", to.Hex(), amountEth, signed.Hash().Hex())
	receipt, err := waitMined(ctx, client, signed.Hash())
	if err != nil {
		return err
	}
	if receipt.Status != 1 {
		return fmt.Errorf("funding tx reverted")
	}
	balance, err := client.BalanceAt(ctx, to, nil)
	if err != nil {
		return err
	}
	fmt.Printf("✅ %s now holds %s wei\n", to.Hex(), balance)
	return nil
}

// reportEntities prints what each validation entity holds and, just as
// importantly, whether its deferred nonce has been spent. An entity that reads
// clear on every module is still unusable once that sequence is non-zero — see
// aa.EntryPointNonce.
func reportEntities(ctx context.Context, client *ethclient.Client, account common.Address) error {
	fmt.Printf("\n%-7s %-44s %s\n", "entity", "signer", "deferredNonceSeq")
	for entity := uint32(1); entity <= 8; entity++ {
		signer, err := aa.EntitySignerOnChain(ctx, client, account, entity)
		if err != nil {
			return err
		}
		sequence, err := aa.EntityDeferredNonceSequence(ctx, client, account, entity)
		if err != nil {
			return err
		}
		state := ""
		switch {
		case signer != (common.Address{}):
			state = "  ← installed"
		case sequence != 0:
			state = "  ← spent; never reissue"
		}
		fmt.Printf("%-7d %-44s %d%s\n", entity, signer.Hex(), sequence, state)
	}
	return nil
}
