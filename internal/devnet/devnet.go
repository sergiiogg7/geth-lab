// Package devnet holds the small pieces shared by every tool in the lab:
// the well-known developer key, deterministic sender keys, a tiny contract,
// and helpers to talk JSON-RPC to a dev node.
//
// Java analogy: think of it as the lab's "commons" module (a util jar that
// every other module depends on). Everything under internal/ can only be
// imported by code inside this module, like a package-private API.
package devnet

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ChainID is the network id geth uses in --dev mode (cmd/utils/flags.go sets
// cfg.NetworkId = 1337 and the dev genesis config uses the same chain id).
const ChainID = 1337

// DevKeyHex is geth's public, hard-coded developer private key
// (cmd/utils/flags.go: DeveloperKey). It is printed in geth's own startup
// banner. It controls the prefunded account of every dev chain. TEST KEY ONLY.
const DevKeyHex = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"

// DevKey returns the developer private key.
func DevKey() *ecdsa.PrivateKey {
	k, err := crypto.HexToECDSA(DevKeyHex)
	if err != nil {
		panic(err) // constant input: cannot happen
	}
	return k
}

// DevAddress is the prefunded account (0x71562b71999873DB5b286dF957af199Ec94617F7).
func DevAddress() common.Address { return crypto.PubkeyToAddress(DevKey().PublicKey) }

// SenderKey derives the i-th deterministic loadgen sender key from a seed.
// Deterministic keys let a second run (e.g. after a restart) find the same
// accounts and their nonces again. TEST KEYS ONLY.
func SenderKey(seed string, i int) *ecdsa.PrivateKey {
	h := crypto.Keccak256([]byte(fmt.Sprintf("geth-lab/%s/%d", seed, i)))
	k, err := crypto.ToECDSA(h)
	if err != nil {
		panic(err)
	}
	return k
}

// CounterInitCode deploys a 8-byte contract whose runtime code is
//
//	PUSH0 SLOAD PUSH1 1 ADD PUSH0 SSTORE STOP     (5f 54 60 01 01 5f 55 00)
//
// i.e. "slot0 = slot0 + 1" regardless of calldata. Hand-assembled so the lab
// does not need solc. The 9-byte constructor copies the runtime code to memory
// and RETURNs it:
//
//	PUSH1 8, DUP1, PUSH1 9, PUSH0, CODECOPY, PUSH0, RETURN
var CounterInitCode = common.FromHex("0x6008806009" + "5f395ff3" + "5f546001015f5500")

// Dial connects an ethclient to a JSON-RPC HTTP endpoint.
func Dial(url string) (*ethclient.Client, error) { return ethclient.Dial(url) }

// WaitReady polls eth_blockNumber until the node answers or the timeout hits.
// It returns how long the node needed, which is the "time to ready" number
// that the chaos postmortems quote.
func WaitReady(url string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		c, err := ethclient.DialContext(ctx, url)
		if err == nil {
			_, err = c.BlockNumber(ctx)
			c.Close()
		}
		cancel()
		if err == nil {
			return time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return time.Since(start), fmt.Errorf("node at %s not ready after %s: %w", url, timeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// SuggestFees returns a (tipCap, feeCap) pair that stays valid for a few
// blocks: feeCap = 2*baseFee + tip. The dev chain base fee starts at 1 gwei
// and decays when blocks are empty, so reading it per call is enough.
func SuggestFees(ctx context.Context, c *ethclient.Client, tip *big.Int) (*big.Int, *big.Int, error) {
	head, err := c.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	base := head.BaseFee
	if base == nil {
		base = new(big.Int)
	}
	feeCap := new(big.Int).Mul(base, big.NewInt(2))
	feeCap.Add(feeCap, tip)
	return new(big.Int).Set(tip), feeCap, nil
}

// SignDynamic builds and signs an EIP-1559 transaction.
func SignDynamic(key *ecdsa.PrivateKey, nonce uint64, to *common.Address, value *big.Int, gas uint64, tip, feeCap *big.Int, data []byte) (*types.Transaction, error) {
	signer := types.LatestSignerForChainID(big.NewInt(ChainID))
	return types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   big.NewInt(ChainID),
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gas,
		To:        to,
		Value:     value,
		Data:      data,
	})
}

// IntrinsicGas computes the minimum gas of a plain call/transfer with the
// given calldata (21000 + 4 per zero byte + 16 per non-zero byte, EIP-2028).
// For a contract call add the execution gas on top (see loadgen).
func IntrinsicGas(data []byte) uint64 {
	gas := uint64(21000)
	for _, b := range data {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}
	return gas
}

// WaitReceipt polls for a transaction receipt.
func WaitReceipt(ctx context.Context, c *ethclient.Client, h common.Hash) (*types.Receipt, error) {
	for {
		r, err := c.TransactionReceipt(ctx, h)
		if err == nil {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
