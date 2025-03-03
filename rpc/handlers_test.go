package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NethermindEth/juno/blockchain"
	"github.com/NethermindEth/juno/clients/feeder"
	"github.com/NethermindEth/juno/core"
	"github.com/NethermindEth/juno/core/felt"
	"github.com/NethermindEth/juno/db"
	"github.com/NethermindEth/juno/db/pebble"
	"github.com/NethermindEth/juno/jsonrpc"
	"github.com/NethermindEth/juno/mocks"
	"github.com/NethermindEth/juno/node"
	"github.com/NethermindEth/juno/rpc"
	"github.com/NethermindEth/juno/starknet"
	adaptfeeder "github.com/NethermindEth/juno/starknetdata/feeder"
	"github.com/NethermindEth/juno/sync"
	"github.com/NethermindEth/juno/utils"
	"github.com/NethermindEth/juno/vm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"nhooyr.io/websocket"
)

func nopCloser() error { return nil }

func TestChainId(t *testing.T) {
	for _, n := range []utils.Network{utils.Mainnet, utils.Goerli, utils.Goerli2, utils.Integration} {
		t.Run(n.String(), func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			t.Cleanup(mockCtrl.Finish)

			mockReader := mocks.NewMockReader(mockCtrl)
			handler := rpc.New(mockReader, nil, n, nil, nil, nil, "", nil)

			cID, err := handler.ChainID()
			require.Nil(t, err)
			assert.Equal(t, n.ChainID(), cID)
		})
	}
}

func TestBlockNumber(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	t.Run("empty blockchain", func(t *testing.T) {
		expectedHeight := uint64(0)
		mockReader.EXPECT().Height().Return(expectedHeight, errors.New("empty blockchain"))

		num, err := handler.BlockNumber()
		assert.Equal(t, expectedHeight, num)
		assert.Equal(t, rpc.ErrNoBlock, err)
	})

	t.Run("blockchain height is 21", func(t *testing.T) {
		expectedHeight := uint64(21)
		mockReader.EXPECT().Height().Return(expectedHeight, nil)

		num, err := handler.BlockNumber()
		require.Nil(t, err)
		assert.Equal(t, expectedHeight, num)
	})
}

func TestBlockHashAndNumber(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().Head().Return(nil, errors.New("empty blockchain"))

		block, err := handler.BlockHashAndNumber()
		assert.Nil(t, block)
		assert.Equal(t, rpc.ErrNoBlock, err)
	})

	t.Run("blockchain height is 147", func(t *testing.T) {
		client := feeder.NewTestClient(t, utils.Mainnet)
		gw := adaptfeeder.New(client)

		expectedBlock, err := gw.BlockByNumber(context.Background(), 147)
		require.NoError(t, err)

		expectedBlockHashAndNumber := &rpc.BlockHashAndNumber{Hash: expectedBlock.Hash, Number: expectedBlock.Number}

		mockReader.EXPECT().Head().Return(expectedBlock, nil)

		hashAndNum, rpcErr := handler.BlockHashAndNumber()
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedBlockHashAndNumber, hashAndNum)
	})
}

func TestBlockTransactionCount(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Goerli, nil, nil, nil, "", nil)

	client := feeder.NewTestClient(t, utils.Goerli)
	gw := adaptfeeder.New(client)

	latestBlockNumber := uint64(485004)
	latestBlock, err := gw.BlockByNumber(context.Background(), latestBlockNumber)
	require.NoError(t, err)
	latestBlockHash := latestBlock.Hash
	expectedCount := latestBlock.TransactionCount

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadsHeader().Return(nil, errors.New("empty blockchain"))

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Latest: true})
		assert.Equal(t, uint64(0), count)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByHash(gomock.Any()).Return(nil, errors.New("block not found"))

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Hash: new(felt.Felt).SetBytes([]byte("random"))})
		assert.Equal(t, uint64(0), count)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(gomock.Any()).Return(nil, errors.New("block not found"))

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Number: uint64(328476)})
		assert.Equal(t, uint64(0), count)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().HeadsHeader().Return(latestBlock.Header, nil)

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedCount, count)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByHash(latestBlockHash).Return(latestBlock.Header, nil)

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Hash: latestBlockHash})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedCount, count)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(latestBlockNumber).Return(latestBlock.Header, nil)

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedCount, count)
	})

	t.Run("blockID - pending", func(t *testing.T) {
		latestBlock.Hash = nil
		latestBlock.GlobalStateRoot = nil
		mockReader.EXPECT().Pending().Return(blockchain.Pending{
			Block: latestBlock,
		}, nil)

		count, rpcErr := handler.BlockTransactionCount(rpc.BlockID{Pending: true})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedCount, count)
	})
}

func TestBlockWithTxHashes(t *testing.T) {
	errTests := map[string]rpc.BlockID{
		"latest":  {Latest: true},
		"pending": {Pending: true},
		"hash":    {Hash: new(felt.Felt).SetUint64(1)},
		"number":  {Number: 1},
	}

	for description, id := range errTests {
		t.Run(description, func(t *testing.T) {
			log := utils.NewNopZapLogger()
			network := utils.Mainnet
			chain := blockchain.New(pebble.NewMemTest(t), network, log)
			handler := rpc.New(chain, nil, network, nil, nil, nil, "", log)

			block, rpcErr := handler.BlockWithTxHashes(id)
			assert.Nil(t, block)
			assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
		})
	}

	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Goerli, nil, nil, nil, "", nil)

	client := feeder.NewTestClient(t, utils.Goerli)
	gw := adaptfeeder.New(client)

	latestBlockNumber := uint64(485004)
	latestBlock, err := gw.BlockByNumber(context.Background(), latestBlockNumber)
	require.NoError(t, err)
	latestBlockHash := latestBlock.Hash

	checkBlock := func(t *testing.T, b *rpc.BlockWithTxHashes) {
		t.Helper()
		assert.Equal(t, latestBlock.Hash, b.Hash)
		assert.Equal(t, latestBlock.GlobalStateRoot, b.NewRoot)
		assert.Equal(t, latestBlock.ParentHash, b.ParentHash)
		assert.Equal(t, latestBlock.SequencerAddress, b.SequencerAddress)
		assert.Equal(t, latestBlock.Timestamp, b.Timestamp)
		assert.Equal(t, len(latestBlock.Transactions), len(b.TxnHashes))
		for i := 0; i < len(latestBlock.Transactions); i++ {
			assert.Equal(t, latestBlock.Transactions[i].Hash(), b.TxnHashes[i])
		}
	}

	checkLatestBlock := func(t *testing.T, b *rpc.BlockWithTxHashes) {
		t.Helper()
		if latestBlock.Hash != nil {
			assert.Equal(t, latestBlock.Number, *b.Number)
		} else {
			assert.Nil(t, b.Number)
			assert.Equal(t, rpc.BlockPending, b.Status)
		}
		checkBlock(t, b)
	}

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().Head().Return(latestBlock, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		block, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, block)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().BlockByHash(latestBlockHash).Return(latestBlock, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		block, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Hash: latestBlockHash})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, block)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().BlockByNumber(latestBlockNumber).Return(latestBlock, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		block, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, block)
	})

	t.Run("blockID - number accepted on l1", func(t *testing.T) {
		mockReader.EXPECT().BlockByNumber(latestBlockNumber).Return(latestBlock, nil)
		mockReader.EXPECT().L1Head().Return(&core.L1Head{
			BlockNumber: latestBlockNumber,
			BlockHash:   latestBlockHash,
			StateRoot:   latestBlock.GlobalStateRoot,
		}, nil)

		block, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		assert.Equal(t, rpc.BlockAcceptedL1, block.Status)
		checkBlock(t, block)
	})

	t.Run("blockID - pending", func(t *testing.T) {
		latestBlock.Hash = nil
		latestBlock.GlobalStateRoot = nil
		mockReader.EXPECT().Pending().Return(blockchain.Pending{
			Block: latestBlock,
		}, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		block, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Pending: true})
		require.Nil(t, rpcErr)
		checkLatestBlock(t, block)
	})
}

func TestBlockWithTxs(t *testing.T) {
	errTests := map[string]rpc.BlockID{
		"latest":  {Latest: true},
		"pending": {Pending: true},
		"hash":    {Hash: new(felt.Felt).SetUint64(1)},
		"number":  {Number: 1},
	}

	for description, id := range errTests {
		t.Run(description, func(t *testing.T) {
			log := utils.NewNopZapLogger()
			network := utils.Mainnet
			chain := blockchain.New(pebble.NewMemTest(t), network, log)
			handler := rpc.New(chain, nil, network, nil, nil, nil, "", log)

			block, rpcErr := handler.BlockWithTxs(id)
			assert.Nil(t, block)
			assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
		})
	}

	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	client := feeder.NewTestClient(t, utils.Mainnet)
	gw := adaptfeeder.New(client)

	latestBlockNumber := uint64(16697)
	latestBlock, err := gw.BlockByNumber(context.Background(), latestBlockNumber)
	require.NoError(t, err)
	latestBlockHash := latestBlock.Hash

	checkLatestBlock := func(t *testing.T, blockWithTxHashes *rpc.BlockWithTxHashes, blockWithTxs *rpc.BlockWithTxs) {
		t.Helper()
		assert.Equal(t, blockWithTxHashes.BlockHeader, blockWithTxs.BlockHeader)
		assert.Equal(t, len(blockWithTxHashes.TxnHashes), len(blockWithTxs.Transactions))

		for i, txnHash := range blockWithTxHashes.TxnHashes {
			txn, err := handler.TransactionByHash(*txnHash)
			require.Nil(t, err)

			assert.Equal(t, txn, blockWithTxs.Transactions[i])
		}
	}

	latestBlockTxMap := make(map[felt.Felt]core.Transaction)
	for _, tx := range latestBlock.Transactions {
		latestBlockTxMap[*tx.Hash()] = tx
	}

	mockReader.EXPECT().TransactionByHash(gomock.Any()).DoAndReturn(func(hash *felt.Felt) (core.Transaction, error) {
		if tx, found := latestBlockTxMap[*hash]; found {
			return tx, nil
		}
		return nil, errors.New("txn not found")
	}).Times(len(latestBlock.Transactions) * 5)

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().Head().Return(latestBlock, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).Times(2)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)

		blockWithTxs, rpcErr := handler.BlockWithTxs(rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, blockWithTxHashes, blockWithTxs)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().BlockByHash(latestBlockHash).Return(latestBlock, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).Times(2)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Hash: latestBlockHash})
		require.Nil(t, rpcErr)

		blockWithTxs, rpcErr := handler.BlockWithTxs(rpc.BlockID{Hash: latestBlockHash})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, blockWithTxHashes, blockWithTxs)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().BlockByNumber(latestBlockNumber).Return(latestBlock, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).Times(2)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		blockWithTxs, rpcErr := handler.BlockWithTxs(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		assert.Equal(t, blockWithTxHashes.BlockHeader, blockWithTxs.BlockHeader)
		assert.Equal(t, len(blockWithTxHashes.TxnHashes), len(blockWithTxs.Transactions))

		checkLatestBlock(t, blockWithTxHashes, blockWithTxs)
	})

	t.Run("blockID - number accepted on l1", func(t *testing.T) {
		mockReader.EXPECT().BlockByNumber(latestBlockNumber).Return(latestBlock, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(&core.L1Head{
			BlockNumber: latestBlockNumber,
			BlockHash:   latestBlockHash,
			StateRoot:   latestBlock.GlobalStateRoot,
		}, nil).Times(2)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		blockWithTxs, rpcErr := handler.BlockWithTxs(rpc.BlockID{Number: latestBlockNumber})
		require.Nil(t, rpcErr)

		assert.Equal(t, blockWithTxHashes.BlockHeader, blockWithTxs.BlockHeader)
		assert.Equal(t, len(blockWithTxHashes.TxnHashes), len(blockWithTxs.Transactions))

		checkLatestBlock(t, blockWithTxHashes, blockWithTxs)
	})

	t.Run("blockID - pending", func(t *testing.T) {
		latestBlock.Hash = nil
		latestBlock.GlobalStateRoot = nil
		mockReader.EXPECT().Pending().Return(blockchain.Pending{
			Block: latestBlock,
		}, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).Times(2)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Pending: true})
		require.Nil(t, rpcErr)

		blockWithTxs, rpcErr := handler.BlockWithTxs(rpc.BlockID{Pending: true})
		require.Nil(t, rpcErr)

		checkLatestBlock(t, blockWithTxHashes, blockWithTxs)
	})
}

func TestBlockWithTxHashesV013(t *testing.T) {
	network := utils.Integration
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, network, nil, nil, nil, "", nil)

	blockNumber := uint64(319132)
	gw := adaptfeeder.New(feeder.NewTestClient(t, network))
	coreBlock, err := gw.BlockByNumber(context.Background(), blockNumber)
	require.NoError(t, err)
	tx, ok := coreBlock.Transactions[0].(*core.InvokeTransaction)
	require.True(t, ok)

	mockReader.EXPECT().BlockByNumber(gomock.Any()).Return(coreBlock, nil)
	mockReader.EXPECT().L1Head().Return(&core.L1Head{}, nil)
	got, rpcErr := handler.BlockWithTxs(rpc.BlockID{Number: blockNumber})
	require.Nil(t, rpcErr)
	got.Transactions = got.Transactions[:1]

	require.Equal(t, &rpc.BlockWithTxs{
		BlockHeader: rpc.BlockHeader{
			Hash:            coreBlock.Hash,
			StarknetVersion: coreBlock.ProtocolVersion,
			NewRoot:         coreBlock.GlobalStateRoot,
			Number:          &coreBlock.Number,
			ParentHash:      coreBlock.ParentHash,
			L1GasPrice: &rpc.ResourcePrice{
				InFri: utils.HexToFelt(t, "0x2540be400"),
				InWei: utils.HexToFelt(t, "0x3b9aca08"),
			},
			SequencerAddress: coreBlock.SequencerAddress,
			Timestamp:        coreBlock.Timestamp,
		},
		Status: rpc.BlockAcceptedL2,
		Transactions: []*rpc.Transaction{
			{
				Hash:               tx.Hash(),
				Type:               rpc.TxnInvoke,
				Version:            tx.Version.AsFelt(),
				Nonce:              tx.Nonce,
				MaxFee:             tx.MaxFee,
				ContractAddress:    tx.ContractAddress,
				SenderAddress:      tx.SenderAddress,
				Signature:          &tx.TransactionSignature,
				CallData:           &tx.CallData,
				EntryPointSelector: tx.EntryPointSelector,
				ResourceBounds: &map[rpc.Resource]rpc.ResourceBounds{
					rpc.ResourceL1Gas: {
						MaxAmount:       new(felt.Felt).SetUint64(tx.ResourceBounds[core.ResourceL1Gas].MaxAmount),
						MaxPricePerUnit: tx.ResourceBounds[core.ResourceL1Gas].MaxPricePerUnit,
					},
					rpc.ResourceL2Gas: {
						MaxAmount:       new(felt.Felt).SetUint64(tx.ResourceBounds[core.ResourceL2Gas].MaxAmount),
						MaxPricePerUnit: tx.ResourceBounds[core.ResourceL2Gas].MaxPricePerUnit,
					},
				},
				Tip:                   new(felt.Felt).SetUint64(tx.Tip),
				PaymasterData:         &tx.PaymasterData,
				AccountDeploymentData: &tx.AccountDeploymentData,
				NonceDAMode:           utils.Ptr(rpc.DataAvailabilityMode(tx.NonceDAMode)),
				FeeDAMode:             utils.Ptr(rpc.DataAvailabilityMode(tx.FeeDAMode)),
			},
		},
	}, got)
}

func TestTransactionByHashNotFound(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	mockReader := mocks.NewMockReader(mockCtrl)

	txHash := new(felt.Felt).SetBytes([]byte("random hash"))
	mockReader.EXPECT().TransactionByHash(txHash).Return(nil, errors.New("tx not found"))

	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	tx, rpcErr := handler.TransactionByHash(*txHash)
	assert.Nil(t, tx)
	assert.Equal(t, rpc.ErrTxnHashNotFound, rpcErr)
}

//nolint:dupl
func TestTransactionByHash(t *testing.T) {
	tests := map[string]struct {
		hash     string
		network  utils.Network
		expected string
	}{
		"DECLARE v1": {
			hash:    "0x1b4d9f09276629d496af1af8ff00173c11ff146affacb1b5c858d7aa89001ae",
			network: utils.Mainnet,
			expected: `{
			"type": "DECLARE",
			"transaction_hash": "0x1b4d9f09276629d496af1af8ff00173c11ff146affacb1b5c858d7aa89001ae",
			"max_fee": "0xf6dbd653833",
			"version": "0x1",
			"signature": [
		"0x221b9576c4f7b46d900a331d89146dbb95a7b03d2eb86b4cdcf11331e4df7f2",
		"0x667d8062f3574ba9b4965871eec1444f80dacfa7114e1d9c74662f5672c0620"
		],
		"nonce": "0x5",
		"class_hash": "0x7aed6898458c4ed1d720d43e342381b25668ec7c3e8837f761051bf4d655e54",
		"sender_address": "0x39291faa79897de1fd6fb1a531d144daa1590d058358171b83eadb3ceafed8"
		}`,
		},

		"DECLARE v0": {
			hash:    "0x222f8902d1eeea76fa2642a90e2411bfd71cffb299b3a299029e1937fab3fe4",
			network: utils.Mainnet,
			expected: `{
				"transaction_hash": "0x222f8902d1eeea76fa2642a90e2411bfd71cffb299b3a299029e1937fab3fe4",
				"type": "DECLARE",
				"max_fee": "0x0",
				"version": "0x0",
				"signature": [],
				"class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0",
				"sender_address": "0x1"
			}`,
		},

		"L1 Handler v0": {
			hash:    "0x537eacfd3c49166eec905daff61ff7feef9c133a049ea2135cb94eec840a4a8",
			network: utils.Mainnet,
			expected: `{
       "type": "L1_HANDLER",
       "transaction_hash": "0x537eacfd3c49166eec905daff61ff7feef9c133a049ea2135cb94eec840a4a8",
       "version": "0x0",
       "nonce": "0x2",
       "contract_address": "0xda8054260ec00606197a4103eb2ef08d6c8af0b6a808b610152d1ce498f8c3",
       "entry_point_selector": "0xc73f681176fc7b3f9693986fd7b14581e8d540519e27400e88b8713932be01",
       "calldata": [
           "0x142273bcbfca76512b2a05aed21f134c4495208",
           "0x160c35f9f962e1bc997f9133d9fb231afd5799f7d63dcbcd506af4866b3874",
           "0x16345785d8a0000",
           "0x0",
           "0x3"
       ]
   }`,
		},

		"Invoke v1": {
			hash:    "0x2897e3cec3e24e4d341df26b8cf1ab84ea1c01a051021836b36c6639145b497",
			network: utils.Mainnet,
			expected: `{
       "type": "INVOKE",
       "transaction_hash": "0x2897e3cec3e24e4d341df26b8cf1ab84ea1c01a051021836b36c6639145b497",
       "max_fee": "0x17f0de82f4be6",
       "version": "0x1",
       "signature": [
           "0x383ba105b6d0f59fab96a412ad267213ddcd899e046278bdba64cd583d680b",
           "0x1896619a17fde468978b8d885ffd6f5c8f4ac1b188233b81b91bcf7dbc56fbd"
       ],
       "nonce": "0x42",
       "sender_address": "0x1fc039de7d864580b57a575e8e6b7114f4d2a954d7d29f876b2eb3dd09394a0",
       "calldata": [
           "0x1",
           "0x727a63f78ee3f1bd18f78009067411ab369c31dece1ae22e16f567906409905",
           "0x22de356837ac200bca613c78bd1fcc962a97770c06625f0c8b3edeb6ae4aa59",
           "0x0",
           "0xb",
           "0xb",
           "0xa",
           "0x6db793d93ce48bc75a5ab02e6a82aad67f01ce52b7b903090725dbc4000eaa2",
           "0x6141eac4031dfb422080ed567fe008fb337b9be2561f479a377aa1de1d1b676",
           "0x27eb1a21fa7593dd12e988c9dd32917a0dea7d77db7e89a809464c09cf951c0",
           "0x400a29400a34d8f69425e1f4335e6a6c24ce1111db3954e4befe4f90ca18eb7",
           "0x599e56821170a12cdcf88fb8714057ce364a8728f738853da61d5b3af08a390",
           "0x46ad66f467df625f3b2dd9d3272e61713e8f74b68adac6718f7497d742cfb17",
           "0x4f348b585e6c1919d524a4bfe6f97230ecb61736fe57534ec42b628f7020849",
           "0x19ae40a095ffe79b0c9fc03df2de0d2ab20f59a2692ed98a8c1062dbf691572",
           "0xe120336994adef6c6e47694f87278686511d4622997d4a6f216bd6e9fa9acc",
           "0x56e6637a4958d062db8c8198e315772819f64d915e5c7a8d58a99fa90ff0742"
       ]
   }`,
		},

		"DEPLOY v0": {
			hash:    "0x6486c6303dba2f364c684a2e9609211c5b8e417e767f37b527cda51e776e6f0",
			network: utils.Mainnet,
			expected: `{
       "type": "DEPLOY",
       "transaction_hash": "0x6486c6303dba2f364c684a2e9609211c5b8e417e767f37b527cda51e776e6f0",
       "version": "0x0",
       "class_hash": "0x46f844ea1a3b3668f81d38b5c1bd55e816e0373802aefe732138628f0133486",
       "contract_address_salt": "0x74dc2fe193daf1abd8241b63329c1123214842b96ad7fd003d25512598a956b",
       "constructor_calldata": [
           "0x6d706cfbac9b8262d601c38251c5fbe0497c3a96cc91a92b08d91b61d9e70c4",
           "0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463",
           "0x2",
           "0x6658165b4984816ab189568637bedec5aa0a18305909c7f5726e4a16e3afef6",
           "0x6b648b36b074a91eee55730f5f5e075ec19c0a8f9ffb0903cefeee93b6ff328"
       ]
   }`,
		},

		"DEPLOY ACCOUNT v1": {
			hash:    "0xd61fc89f4d1dc4dc90a014957d655d38abffd47ecea8e3fa762e3160f155f2",
			network: utils.Mainnet,
			expected: `{
       "type": "DEPLOY_ACCOUNT",
       "transaction_hash": "0xd61fc89f4d1dc4dc90a014957d655d38abffd47ecea8e3fa762e3160f155f2",
       "max_fee": "0xb5e620f48000",
       "version": "0x1",
       "signature": [
           "0x41c3543008dd65ed98c767e5d218b0c0ce1bd0cd60877824951a6f87cc1637d",
           "0x7f803845aa7e43d183fd05cd553c64711b1c49af69a155fe8144e8da9a5a50d"
       ],
       "nonce": "0x0",
       "class_hash": "0x1fac3074c9d5282f0acc5c69a4781a1c711efea5e73c550c5d9fb253cf7fd3d",
       "contract_address_salt": "0x14e2ae44cbb50dff0e18140e7c415c1f281207d06fd6a0106caf3ff21e130d8",
       "constructor_calldata": [
           "0x6113c1775f3d0fda0b45efbb69f6e2306da3c174df523ef0acdd372bf0a61cb"
       ]
   }`,
		},

		"INVOKE v0": {
			hash:    "0xf1d99fb97509e0dfc425ddc2a8c5398b74231658ca58b6f8da92f39cb739e",
			network: utils.Mainnet,
			expected: `{
       "type": "INVOKE",
       "transaction_hash": "0xf1d99fb97509e0dfc425ddc2a8c5398b74231658ca58b6f8da92f39cb739e",
       "max_fee": "0x0",
       "version": "0x0",
       "signature": [],
       "contract_address": "0x43324c97e376d7d164abded1af1e73e9ce8214249f711edb7059c1ca34560e8",
       "entry_point_selector": "0x317eb442b72a9fae758d4fb26830ed0d9f31c8e7da4dbff4e8c59ea6a158e7f",
       "calldata": [
           "0x1b654cb59f978da2eee76635158e5ff1399bf607cb2d05e3e3b4e41d7660ca2",
           "0x2",
           "0x5f743efdb29609bfc2002041bdd5c72257c0c6b5c268fc929a3e516c171c731",
           "0x635afb0ea6c4cdddf93f42287b45b67acee4f08c6f6c53589e004e118491546"
       ]
   }`,
		},
		"DECLARE v3": {
			hash:    "0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3",
			network: utils.Integration,
			expected: `{
		"transaction_hash": "0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3",
		"type": "DECLARE",
		"version": "0x3",
		"nonce": "0x1",
		"sender_address": "0x2fab82e4aef1d8664874e1f194951856d48463c3e6bf9a8c68e234a629a6f50",
		"class_hash": "0x5ae9d09292a50ed48c5930904c880dab56e85b825022a7d689cfc9e65e01ee7",
		"compiled_class_hash": "0x1add56d64bebf8140f3b8a38bdf102b7874437f0c861ab4ca7526ec33b4d0f8",
		"signature": [
			"0x29a49dff154fede73dd7b5ca5a0beadf40b4b069f3a850cd8428e54dc809ccc",
			"0x429d142a17223b4f2acde0f5ecb9ad453e188b245003c86fab5c109bad58fc3"
		],
		"resource_bounds": {
			"l1_gas": {
				"max_amount": "0x186a0",
				"max_price_per_unit": "0x2540be400"
			},
			"l2_gas": { "max_amount": "0x0", "max_price_per_unit": "0x0" }
		},
		"tip": "0x0",
		"paymaster_data": [],
		"account_deployment_data": [],
		"nonce_data_availability_mode": "L1",
		"fee_data_availability_mode": "L1"
	   }`,
		},
		"INVOKE v3": {
			hash:    "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
			network: utils.Integration,
			expected: `{
				"type": "INVOKE",
				"transaction_hash": "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
				"version": "0x3",
				"signature": [
					"0x71a9b2cd8a8a6a4ca284dcddcdefc6c4fd20b92c1b201bd9836e4ce376fad16",
					"0x6bef4745194c9447fdc8dd3aec4fc738ab0a560b0d2c7bf62fbf58aef3abfc5"
				],
				"nonce": "0xe97",
				"resource_bounds": {
					"l1_gas": {
						"max_amount": "0x186a0",
						"max_price_per_unit": "0x5af3107a4000"
					},
					"l2_gas": { "max_amount": "0x0", "max_price_per_unit": "0x0" }
				},
				"tip": "0x0",
				"paymaster_data": [],
				"sender_address": "0x3f6f3bc663aedc5285d6013cc3ffcbc4341d86ab488b8b68d297f8258793c41",
				"calldata": [
					"0x2",
					"0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
					"0x27c3334165536f239cfd400ed956eabff55fc60de4fb56728b6a4f6b87db01c",
					"0x0",
					"0x4",
					"0x4c312760dfd17a954cdd09e76aa9f149f806d88ec3e402ffaf5c4926f568a42",
					"0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
					"0x4",
					"0x1",
					"0x5",
					"0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
					"0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
					"0x1",
					"0x7fe4fd616c7fece1244b3616bb516562e230be8c9f29668b46ce0369d5ca829",
					"0x287acddb27a2f9ba7f2612d72788dc96a5b30e401fc1e8072250940e024a587"
				],
				"account_deployment_data": [],
				"nonce_data_availability_mode": "L1",
				"fee_data_availability_mode": "L1"
			}`,
		},
		"DEPLOY ACCOUNT v3": {
			hash:    "0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0",
			network: utils.Integration,
			expected: `{
				"transaction_hash": "0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0",
				"version": "0x3",
				"signature": [
					"0x6d756e754793d828c6c1a89c13f7ec70dbd8837dfeea5028a673b80e0d6b4ec",
					"0x4daebba599f860daee8f6e100601d98873052e1c61530c630cc4375c6bd48e3"
				],
				"nonce": "0x0",
				"resource_bounds": {
					"l1_gas": {
						"max_amount": "0x186a0",
						"max_price_per_unit": "0x5af3107a4000"
					},
					"l2_gas": { "max_amount": "0x0", "max_price_per_unit": "0x0" }
				},
				"tip": "0x0",
				"paymaster_data": [],
				"contract_address_salt": "0x0",
				"class_hash": "0x2338634f11772ea342365abd5be9d9dc8a6f44f159ad782fdebd3db5d969738",
				"constructor_calldata": [
					"0x5cd65f3d7daea6c63939d659b8473ea0c5cd81576035a4d34e52fb06840196c"
				],
				"type": "DEPLOY_ACCOUNT",
				"nonce_data_availability_mode": "L1",
				"fee_data_availability_mode": "L1"
			}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw := adaptfeeder.New(feeder.NewTestClient(t, test.network))
			mockCtrl := gomock.NewController(t)
			t.Cleanup(mockCtrl.Finish)
			mockReader := mocks.NewMockReader(mockCtrl)
			mockReader.EXPECT().TransactionByHash(gomock.Any()).DoAndReturn(func(hash *felt.Felt) (core.Transaction, error) {
				return gw.Transaction(context.Background(), hash)
			}).Times(1)
			handler := rpc.New(mockReader, nil, test.network, nil, nil, nil, "", nil)

			hash, err := new(felt.Felt).SetString(test.hash)
			require.NoError(t, err)

			expectedMap := make(map[string]any)
			require.NoError(t, json.Unmarshal([]byte(test.expected), &expectedMap))

			res, rpcErr := handler.TransactionByHash(*hash)
			require.Nil(t, rpcErr)

			resJSON, err := json.Marshal(res)
			require.NoError(t, err)
			resMap := make(map[string]any)
			require.NoError(t, json.Unmarshal(resJSON, &resMap))

			assert.Equal(t, expectedMap, resMap, string(resJSON))
		})
	}
}

//nolint:dupl
func TestLegacyTransactionByHash(t *testing.T) {
	tests := map[string]struct {
		hash     string
		network  utils.Network
		expected string
	}{
		"DECLARE v1": {
			hash:    "0x1b4d9f09276629d496af1af8ff00173c11ff146affacb1b5c858d7aa89001ae",
			network: utils.Mainnet,
			expected: `{
			"type": "DECLARE",
			"transaction_hash": "0x1b4d9f09276629d496af1af8ff00173c11ff146affacb1b5c858d7aa89001ae",
			"max_fee": "0xf6dbd653833",
			"version": "0x1",
			"signature": [
		"0x221b9576c4f7b46d900a331d89146dbb95a7b03d2eb86b4cdcf11331e4df7f2",
		"0x667d8062f3574ba9b4965871eec1444f80dacfa7114e1d9c74662f5672c0620"
		],
		"nonce": "0x5",
		"class_hash": "0x7aed6898458c4ed1d720d43e342381b25668ec7c3e8837f761051bf4d655e54",
		"sender_address": "0x39291faa79897de1fd6fb1a531d144daa1590d058358171b83eadb3ceafed8"
		}`,
		},

		"DECLARE v0": {
			hash:    "0x222f8902d1eeea76fa2642a90e2411bfd71cffb299b3a299029e1937fab3fe4",
			network: utils.Mainnet,
			expected: `{
				"transaction_hash": "0x222f8902d1eeea76fa2642a90e2411bfd71cffb299b3a299029e1937fab3fe4",
				"type": "DECLARE",
				"max_fee": "0x0",
				"version": "0x0",
				"signature": [],
				"class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0",
				"sender_address": "0x1"
			}`,
		},

		"L1 Handler v0": {
			hash:    "0x537eacfd3c49166eec905daff61ff7feef9c133a049ea2135cb94eec840a4a8",
			network: utils.Mainnet,
			expected: `{
       "type": "L1_HANDLER",
       "transaction_hash": "0x537eacfd3c49166eec905daff61ff7feef9c133a049ea2135cb94eec840a4a8",
       "version": "0x0",
       "nonce": "0x2",
       "contract_address": "0xda8054260ec00606197a4103eb2ef08d6c8af0b6a808b610152d1ce498f8c3",
       "entry_point_selector": "0xc73f681176fc7b3f9693986fd7b14581e8d540519e27400e88b8713932be01",
       "calldata": [
           "0x142273bcbfca76512b2a05aed21f134c4495208",
           "0x160c35f9f962e1bc997f9133d9fb231afd5799f7d63dcbcd506af4866b3874",
           "0x16345785d8a0000",
           "0x0",
           "0x3"
       ]
   }`,
		},

		"Invoke v1": {
			hash:    "0x2897e3cec3e24e4d341df26b8cf1ab84ea1c01a051021836b36c6639145b497",
			network: utils.Mainnet,
			expected: `{
       "type": "INVOKE",
       "transaction_hash": "0x2897e3cec3e24e4d341df26b8cf1ab84ea1c01a051021836b36c6639145b497",
       "max_fee": "0x17f0de82f4be6",
       "version": "0x1",
       "signature": [
           "0x383ba105b6d0f59fab96a412ad267213ddcd899e046278bdba64cd583d680b",
           "0x1896619a17fde468978b8d885ffd6f5c8f4ac1b188233b81b91bcf7dbc56fbd"
       ],
       "nonce": "0x42",
       "sender_address": "0x1fc039de7d864580b57a575e8e6b7114f4d2a954d7d29f876b2eb3dd09394a0",
       "calldata": [
           "0x1",
           "0x727a63f78ee3f1bd18f78009067411ab369c31dece1ae22e16f567906409905",
           "0x22de356837ac200bca613c78bd1fcc962a97770c06625f0c8b3edeb6ae4aa59",
           "0x0",
           "0xb",
           "0xb",
           "0xa",
           "0x6db793d93ce48bc75a5ab02e6a82aad67f01ce52b7b903090725dbc4000eaa2",
           "0x6141eac4031dfb422080ed567fe008fb337b9be2561f479a377aa1de1d1b676",
           "0x27eb1a21fa7593dd12e988c9dd32917a0dea7d77db7e89a809464c09cf951c0",
           "0x400a29400a34d8f69425e1f4335e6a6c24ce1111db3954e4befe4f90ca18eb7",
           "0x599e56821170a12cdcf88fb8714057ce364a8728f738853da61d5b3af08a390",
           "0x46ad66f467df625f3b2dd9d3272e61713e8f74b68adac6718f7497d742cfb17",
           "0x4f348b585e6c1919d524a4bfe6f97230ecb61736fe57534ec42b628f7020849",
           "0x19ae40a095ffe79b0c9fc03df2de0d2ab20f59a2692ed98a8c1062dbf691572",
           "0xe120336994adef6c6e47694f87278686511d4622997d4a6f216bd6e9fa9acc",
           "0x56e6637a4958d062db8c8198e315772819f64d915e5c7a8d58a99fa90ff0742"
       ]
   }`,
		},

		"DEPLOY v0": {
			hash:    "0x6486c6303dba2f364c684a2e9609211c5b8e417e767f37b527cda51e776e6f0",
			network: utils.Mainnet,
			expected: `{
       "type": "DEPLOY",
       "transaction_hash": "0x6486c6303dba2f364c684a2e9609211c5b8e417e767f37b527cda51e776e6f0",
       "version": "0x0",
       "class_hash": "0x46f844ea1a3b3668f81d38b5c1bd55e816e0373802aefe732138628f0133486",
       "contract_address_salt": "0x74dc2fe193daf1abd8241b63329c1123214842b96ad7fd003d25512598a956b",
       "constructor_calldata": [
           "0x6d706cfbac9b8262d601c38251c5fbe0497c3a96cc91a92b08d91b61d9e70c4",
           "0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463",
           "0x2",
           "0x6658165b4984816ab189568637bedec5aa0a18305909c7f5726e4a16e3afef6",
           "0x6b648b36b074a91eee55730f5f5e075ec19c0a8f9ffb0903cefeee93b6ff328"
       ]
   }`,
		},

		"DEPLOY ACCOUNT v1": {
			hash:    "0xd61fc89f4d1dc4dc90a014957d655d38abffd47ecea8e3fa762e3160f155f2",
			network: utils.Mainnet,
			expected: `{
       "type": "DEPLOY_ACCOUNT",
       "transaction_hash": "0xd61fc89f4d1dc4dc90a014957d655d38abffd47ecea8e3fa762e3160f155f2",
       "max_fee": "0xb5e620f48000",
       "version": "0x1",
       "signature": [
           "0x41c3543008dd65ed98c767e5d218b0c0ce1bd0cd60877824951a6f87cc1637d",
           "0x7f803845aa7e43d183fd05cd553c64711b1c49af69a155fe8144e8da9a5a50d"
       ],
       "nonce": "0x0",
       "class_hash": "0x1fac3074c9d5282f0acc5c69a4781a1c711efea5e73c550c5d9fb253cf7fd3d",
       "contract_address_salt": "0x14e2ae44cbb50dff0e18140e7c415c1f281207d06fd6a0106caf3ff21e130d8",
       "constructor_calldata": [
           "0x6113c1775f3d0fda0b45efbb69f6e2306da3c174df523ef0acdd372bf0a61cb"
       ]
   }`,
		},

		"INVOKE v0": {
			hash:    "0xf1d99fb97509e0dfc425ddc2a8c5398b74231658ca58b6f8da92f39cb739e",
			network: utils.Mainnet,
			expected: `{
       "type": "INVOKE",
       "transaction_hash": "0xf1d99fb97509e0dfc425ddc2a8c5398b74231658ca58b6f8da92f39cb739e",
       "max_fee": "0x0",
       "version": "0x0",
       "signature": [],
       "contract_address": "0x43324c97e376d7d164abded1af1e73e9ce8214249f711edb7059c1ca34560e8",
       "entry_point_selector": "0x317eb442b72a9fae758d4fb26830ed0d9f31c8e7da4dbff4e8c59ea6a158e7f",
       "calldata": [
           "0x1b654cb59f978da2eee76635158e5ff1399bf607cb2d05e3e3b4e41d7660ca2",
           "0x2",
           "0x5f743efdb29609bfc2002041bdd5c72257c0c6b5c268fc929a3e516c171c731",
           "0x635afb0ea6c4cdddf93f42287b45b67acee4f08c6f6c53589e004e118491546"
       ]
   }`,
		},
		"DECLARE v3": {
			hash:    "0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3",
			network: utils.Integration,
			expected: `{
		"transaction_hash": "0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3",
		"type": "DECLARE",
		"version": "0x2",
		"nonce": "0x1",
		"sender_address": "0x2fab82e4aef1d8664874e1f194951856d48463c3e6bf9a8c68e234a629a6f50",
		"class_hash": "0x5ae9d09292a50ed48c5930904c880dab56e85b825022a7d689cfc9e65e01ee7",
		"compiled_class_hash": "0x1add56d64bebf8140f3b8a38bdf102b7874437f0c861ab4ca7526ec33b4d0f8",
		"signature": [
			"0x29a49dff154fede73dd7b5ca5a0beadf40b4b069f3a850cd8428e54dc809ccc",
			"0x429d142a17223b4f2acde0f5ecb9ad453e188b245003c86fab5c109bad58fc3"
		],
		"max_fee": "0x38d7ea4c68000"
	   }`,
		},
		"INVOKE v3": {
			hash:    "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
			network: utils.Integration,
			expected: `{
				"type": "INVOKE",
				"transaction_hash": "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
				"version": "0x1",
				"signature": [
					"0x71a9b2cd8a8a6a4ca284dcddcdefc6c4fd20b92c1b201bd9836e4ce376fad16",
					"0x6bef4745194c9447fdc8dd3aec4fc738ab0a560b0d2c7bf62fbf58aef3abfc5"
				],
				"nonce": "0xe97",
				"max_fee": "0x8ac7230489e80000",
				"sender_address": "0x3f6f3bc663aedc5285d6013cc3ffcbc4341d86ab488b8b68d297f8258793c41",
				"calldata": [
					"0x2",
					"0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
					"0x27c3334165536f239cfd400ed956eabff55fc60de4fb56728b6a4f6b87db01c",
					"0x0",
					"0x4",
					"0x4c312760dfd17a954cdd09e76aa9f149f806d88ec3e402ffaf5c4926f568a42",
					"0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
					"0x4",
					"0x1",
					"0x5",
					"0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
					"0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
					"0x1",
					"0x7fe4fd616c7fece1244b3616bb516562e230be8c9f29668b46ce0369d5ca829",
					"0x287acddb27a2f9ba7f2612d72788dc96a5b30e401fc1e8072250940e024a587"
				]
			}`,
		},
		"DEPLOY ACCOUNT v3": {
			hash:    "0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0",
			network: utils.Integration,
			expected: `{
				"transaction_hash": "0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0",
				"version": "0x1",
				"signature": [
					"0x6d756e754793d828c6c1a89c13f7ec70dbd8837dfeea5028a673b80e0d6b4ec",
					"0x4daebba599f860daee8f6e100601d98873052e1c61530c630cc4375c6bd48e3"
				],
				"nonce": "0x0",
				"max_fee": "0x8ac7230489e80000",
				"contract_address_salt": "0x0",
				"class_hash": "0x2338634f11772ea342365abd5be9d9dc8a6f44f159ad782fdebd3db5d969738",
				"constructor_calldata": [
					"0x5cd65f3d7daea6c63939d659b8473ea0c5cd81576035a4d34e52fb06840196c"
				],
				"type": "DEPLOY_ACCOUNT"
			}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw := adaptfeeder.New(feeder.NewTestClient(t, test.network))
			mockCtrl := gomock.NewController(t)
			t.Cleanup(mockCtrl.Finish)
			mockReader := mocks.NewMockReader(mockCtrl)
			mockReader.EXPECT().TransactionByHash(gomock.Any()).DoAndReturn(func(hash *felt.Felt) (core.Transaction, error) {
				return gw.Transaction(context.Background(), hash)
			}).Times(1)
			handler := rpc.New(mockReader, nil, test.network, nil, nil, nil, "", nil)

			hash, err := new(felt.Felt).SetString(test.hash)
			require.NoError(t, err)

			expectedMap := make(map[string]any)
			require.NoError(t, json.Unmarshal([]byte(test.expected), &expectedMap))

			res, rpcErr := handler.LegacyTransactionByHash(*hash)
			require.Nil(t, rpcErr)

			resJSON, err := json.Marshal(res)
			require.NoError(t, err)
			resMap := make(map[string]any)
			require.NoError(t, json.Unmarshal(resJSON, &resMap))

			assert.Equal(t, expectedMap, resMap, string(resJSON))
		})
	}
}

func TestTransactionByBlockIdAndIndex(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	client := feeder.NewTestClient(t, utils.Mainnet)
	mainnetGw := adaptfeeder.New(client)

	latestBlockNumber := 19199
	latestBlock, err := mainnetGw.BlockByNumber(context.Background(), 19199)
	require.NoError(t, err)
	latestBlockHash := latestBlock.Hash

	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadsHeader().Return(nil, errors.New("empty blockchain"))

		txn, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Latest: true}, rand.Int())
		assert.Nil(t, txn)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByHash(gomock.Any()).Return(nil, errors.New("block not found"))

		txn, rpcErr := handler.TransactionByBlockIDAndIndex(
			rpc.BlockID{Hash: new(felt.Felt).SetBytes([]byte("random"))}, rand.Int())
		assert.Nil(t, txn)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(gomock.Any()).Return(nil, errors.New("block not found"))

		txn, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Number: rand.Uint64()}, rand.Int())
		assert.Nil(t, txn)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("negative index", func(t *testing.T) {
		txn, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Latest: true}, -1)
		assert.Nil(t, txn)
		assert.Equal(t, rpc.ErrInvalidTxIndex, rpcErr)
	})

	t.Run("invalid index", func(t *testing.T) {
		mockReader.EXPECT().HeadsHeader().Return(latestBlock.Header, nil)
		mockReader.EXPECT().TransactionByBlockNumberAndIndex(uint64(latestBlockNumber),
			latestBlock.TransactionCount).Return(nil, errors.New("invalid index"))

		txn, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Latest: true}, len(latestBlock.Transactions))
		assert.Nil(t, txn)
		assert.Equal(t, rpc.ErrInvalidTxIndex, rpcErr)
	})

	t.Run("blockID - latest", func(t *testing.T) {
		index := rand.Intn(int(latestBlock.TransactionCount))

		mockReader.EXPECT().HeadsHeader().Return(latestBlock.Header, nil)
		mockReader.EXPECT().TransactionByBlockNumberAndIndex(uint64(latestBlockNumber),
			uint64(index)).DoAndReturn(func(number, index uint64) (core.Transaction, error) {
			return latestBlock.Transactions[index], nil
		})
		mockReader.EXPECT().TransactionByHash(latestBlock.Transactions[index].Hash()).DoAndReturn(
			func(hash *felt.Felt) (core.Transaction, error) {
				return latestBlock.Transactions[index], nil
			})

		txn1, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Latest: true}, index)
		require.Nil(t, rpcErr)

		txn2, rpcErr := handler.TransactionByHash(*latestBlock.Transactions[index].Hash())
		require.Nil(t, rpcErr)

		assert.Equal(t, txn1, txn2)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		index := rand.Intn(int(latestBlock.TransactionCount))

		mockReader.EXPECT().BlockHeaderByHash(latestBlockHash).Return(latestBlock.Header, nil)
		mockReader.EXPECT().TransactionByBlockNumberAndIndex(uint64(latestBlockNumber),
			uint64(index)).DoAndReturn(func(number, index uint64) (core.Transaction, error) {
			return latestBlock.Transactions[index], nil
		})
		mockReader.EXPECT().TransactionByHash(latestBlock.Transactions[index].Hash()).DoAndReturn(
			func(hash *felt.Felt) (core.Transaction, error) {
				return latestBlock.Transactions[index], nil
			})

		txn1, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Hash: latestBlockHash}, index)
		require.Nil(t, rpcErr)

		txn2, rpcErr := handler.TransactionByHash(*latestBlock.Transactions[index].Hash())
		require.Nil(t, rpcErr)

		assert.Equal(t, txn1, txn2)
	})

	t.Run("blockID - number", func(t *testing.T) {
		index := rand.Intn(int(latestBlock.TransactionCount))

		mockReader.EXPECT().BlockHeaderByNumber(uint64(latestBlockNumber)).Return(latestBlock.Header, nil)
		mockReader.EXPECT().TransactionByBlockNumberAndIndex(uint64(latestBlockNumber),
			uint64(index)).DoAndReturn(func(number, index uint64) (core.Transaction, error) {
			return latestBlock.Transactions[index], nil
		})
		mockReader.EXPECT().TransactionByHash(latestBlock.Transactions[index].Hash()).DoAndReturn(
			func(hash *felt.Felt) (core.Transaction, error) {
				return latestBlock.Transactions[index], nil
			})

		txn1, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Number: uint64(latestBlockNumber)}, index)
		require.Nil(t, rpcErr)

		txn2, rpcErr := handler.TransactionByHash(*latestBlock.Transactions[index].Hash())
		require.Nil(t, rpcErr)

		assert.Equal(t, txn1, txn2)
	})

	t.Run("blockID - pending", func(t *testing.T) {
		index := rand.Intn(int(latestBlock.TransactionCount))

		latestBlock.Hash = nil
		latestBlock.GlobalStateRoot = nil
		mockReader.EXPECT().Pending().Return(blockchain.Pending{
			Block: latestBlock,
		}, nil)
		mockReader.EXPECT().TransactionByHash(latestBlock.Transactions[index].Hash()).DoAndReturn(
			func(hash *felt.Felt) (core.Transaction, error) {
				return latestBlock.Transactions[index], nil
			})

		txn1, rpcErr := handler.TransactionByBlockIDAndIndex(rpc.BlockID{Pending: true}, index)
		require.Nil(t, rpcErr)

		txn2, rpcErr := handler.TransactionByHash(*latestBlock.Transactions[index].Hash())
		require.Nil(t, rpcErr)

		assert.Equal(t, txn1, txn2)
	})
}

//nolint:dupl
func TestTransactionReceiptByHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	t.Run("transaction not found", func(t *testing.T) {
		txHash := new(felt.Felt).SetBytes([]byte("random hash"))
		mockReader.EXPECT().TransactionByHash(txHash).Return(nil, errors.New("tx not found"))

		tx, rpcErr := handler.TransactionReceiptByHash(*txHash)
		assert.Nil(t, tx)
		assert.Equal(t, rpc.ErrTxnHashNotFound, rpcErr)
	})

	client := feeder.NewTestClient(t, utils.Mainnet)
	mainnetGw := adaptfeeder.New(client)

	block0, err := mainnetGw.BlockByNumber(context.Background(), 0)
	require.NoError(t, err)

	checkTxReceipt := func(t *testing.T, h *felt.Felt, expected string) {
		t.Helper()

		expectedMap := make(map[string]any)
		require.NoError(t, json.Unmarshal([]byte(expected), &expectedMap))

		receipt, err := handler.TransactionReceiptByHash(*h)
		require.Nil(t, err)

		receiptJSON, jsonErr := json.Marshal(receipt)
		require.NoError(t, jsonErr)

		receiptMap := make(map[string]any)
		require.NoError(t, json.Unmarshal(receiptJSON, &receiptMap))
		assert.Equal(t, expectedMap, receiptMap)
	}

	tests := map[string]struct {
		index    int
		expected string
	}{
		"with contract addr": {
			index: 0,
			expected: `{
					"type": "DEPLOY",
					"transaction_hash": "0xe0a2e45a80bb827967e096bcf58874f6c01c191e0a0530624cba66a508ae75",
					"actual_fee": {"amount": "0x0", "unit": "WEI"},
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [],
					"events": [],
					"contract_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
					"execution_resources":{"steps":29}
				}`,
		},
		"without contract addr": {
			index: 2,
			expected: `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": {"amount": "0x0", "unit": "WEI"},
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"steps":31}
				}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			txHash := block0.Transactions[test.index].Hash()
			mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[test.index], nil)
			mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[test.index], block0.Hash, block0.Number, nil)
			mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

			checkTxReceipt(t, txHash, test.expected)
		})
	}

	t.Run("pending receipt", func(t *testing.T) {
		i := 2
		expected := `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": {"amount": "0x0", "unit": "WEI"},
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"steps":31}
				}`

		txHash := block0.Transactions[i].Hash()
		mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[i], nil)
		mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[i], nil, uint64(0), nil)

		checkTxReceipt(t, txHash, expected)
	})

	t.Run("accepted on l1 receipt", func(t *testing.T) {
		i := 2
		expected := `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": {"amount": "0x0", "unit": "WEI"},
					"finality_status": "ACCEPTED_ON_L1",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"steps":31}
				}`

		txHash := block0.Transactions[i].Hash()
		mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[i], nil)
		mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[i], block0.Hash, block0.Number, nil)
		mockReader.EXPECT().L1Head().Return(&core.L1Head{
			BlockNumber: block0.Number,
			BlockHash:   block0.Hash,
			StateRoot:   block0.GlobalStateRoot,
		}, nil)

		checkTxReceipt(t, txHash, expected)
	})
	t.Run("reverted", func(t *testing.T) {
		expected := `{
			"type": "INVOKE",
			"transaction_hash": "0x19abec18bbacec23c2eee160c70190a48e4b41dd5ff98ad8f247f9393559998",
			"actual_fee": {"amount": "0x247aff6e224", "unit": "WEI"},
			"execution_status": "REVERTED",
			"finality_status": "ACCEPTED_ON_L2",
			"block_hash": "0x76e0229fd0c36dda2ee7905f7e4c9b3ebb78d98c4bfab550bcb3a03bf859a6",
			"block_number": 304740,
			"messages_sent": [],
			"events": [],
			"revert_reason": "Error in the called contract (0x00b1461de04c6a1aa3375bdf9b7723a8779c082ffe21311d683a0b15c078b5dc):\nError at pc=0:25:\nGot an exception while executing a hint.\nCairo traceback (most recent call last):\nUnknown location (pc=0:731)\nUnknown location (pc=0:677)\nUnknown location (pc=0:291)\nUnknown location (pc=0:314)\n\nError in the called contract (0x049d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7):\nError at pc=0:104:\nGot an exception while executing a hint.\nCairo traceback (most recent call last):\nUnknown location (pc=0:1678)\nUnknown location (pc=0:1664)\n\nError in the called contract (0x049d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7):\nError at pc=0:6:\nGot an exception while executing a hint: Assertion failed, 0 % 0x800000000000011000000000000000000000000000000000000000000000001 is equal to 0\nCairo traceback (most recent call last):\nUnknown location (pc=0:1238)\nUnknown location (pc=0:1215)\nUnknown location (pc=0:836)\n",
			"execution_resources":{"steps":0}
		}`

		integClient := feeder.NewTestClient(t, utils.Integration)
		integGw := adaptfeeder.New(integClient)

		blockWithRevertedTxn, err := integGw.BlockByNumber(context.Background(), 304740)
		require.NoError(t, err)

		revertedTxnIdx := 1
		revertedTxnHash := blockWithRevertedTxn.Transactions[revertedTxnIdx].Hash()

		mockReader.EXPECT().TransactionByHash(revertedTxnHash).Return(blockWithRevertedTxn.Transactions[revertedTxnIdx], nil)
		mockReader.EXPECT().Receipt(revertedTxnHash).Return(blockWithRevertedTxn.Receipts[revertedTxnIdx],
			blockWithRevertedTxn.Hash, blockWithRevertedTxn.Number, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		checkTxReceipt(t, revertedTxnHash, expected)
	})

	t.Run("v3 tx", func(t *testing.T) {
		expected := `{
			"block_hash": "0x50e864db6b81ce69fbeb70e6a7284ee2febbb9a2e707415de7adab83525e9cd",
			"block_number": 319132,
			"execution_status": "SUCCEEDED",
			"finality_status": "ACCEPTED_ON_L2",
			"transaction_hash": "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
			"messages_sent": [],
			"events": [
				{
					"from_address": "0x4718f5a0fc34cc1af16a1cdee98ffb20c31f5cd61d6ab07201858f4287c938d",
					"keys": [
						"0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"
					],
					"data": [
						"0x3f6f3bc663aedc5285d6013cc3ffcbc4341d86ab488b8b68d297f8258793c41",
						"0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8",
						"0x16d8b4ad4000",
						"0x0"
					]
				},
				{
					"from_address": "0x4718f5a0fc34cc1af16a1cdee98ffb20c31f5cd61d6ab07201858f4287c938d",
					"keys": [
						"0xa9fa878c35cd3d0191318f89033ca3e5501a3d90e21e3cc9256bdd5cd17fdd"
					],
					"data": [
						"0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8",
						"0x18ad8494375bc00",
						"0x0",
						"0x18aef21f822fc00",
						"0x0"
					]
				}
			],
			"execution_resources": {
				"steps": 615,
				"range_check_builtin_applications": 19,
				"memory_holes": 4
			},
			"actual_fee": {
				"amount": "0x16d8b4ad4000",
				"unit": "FRI"
			},
			"type": "INVOKE"
		}`

		integClient := feeder.NewTestClient(t, utils.Integration)
		integGw := adaptfeeder.New(integClient)

		block, err := integGw.BlockByNumber(context.Background(), 319132)
		require.NoError(t, err)

		index := 0
		txnHash := block.Transactions[index].Hash()

		mockReader.EXPECT().TransactionByHash(txnHash).Return(block.Transactions[index], nil)
		mockReader.EXPECT().Receipt(txnHash).Return(block.Receipts[index],
			block.Hash, block.Number, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		checkTxReceipt(t, txnHash, expected)
	})
}

//nolint:dupl
func TestLegacyTransactionReceiptByHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	t.Run("transaction not found", func(t *testing.T) {
		txHash := new(felt.Felt).SetBytes([]byte("random hash"))
		mockReader.EXPECT().TransactionByHash(txHash).Return(nil, errors.New("tx not found"))

		tx, rpcErr := handler.TransactionReceiptByHash(*txHash)
		assert.Nil(t, tx)
		assert.Equal(t, rpc.ErrTxnHashNotFound, rpcErr)
	})

	client := feeder.NewTestClient(t, utils.Mainnet)
	mainnetGw := adaptfeeder.New(client)

	block0, err := mainnetGw.BlockByNumber(context.Background(), 0)
	require.NoError(t, err)

	checkTxReceipt := func(t *testing.T, h *felt.Felt, expected string) {
		t.Helper()

		expectedMap := make(map[string]any)
		require.NoError(t, json.Unmarshal([]byte(expected), &expectedMap))

		receipt, err := handler.LegacyTransactionReceiptByHash(*h)
		require.Nil(t, err)

		receiptJSON, jsonErr := json.Marshal(receipt)
		require.NoError(t, jsonErr)

		receiptMap := make(map[string]any)
		require.NoError(t, json.Unmarshal(receiptJSON, &receiptMap))
		assert.Equal(t, expectedMap, receiptMap)
	}

	tests := map[string]struct {
		index    int
		expected string
	}{
		"with contract addr": {
			index: 0,
			expected: `{
					"type": "DEPLOY",
					"transaction_hash": "0xe0a2e45a80bb827967e096bcf58874f6c01c191e0a0530624cba66a508ae75",
					"actual_fee": "0x0",
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [],
					"events": [],
					"contract_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
					"execution_resources": {"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x0", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x0", "steps":"0x1d"}
				}`,
		},
		"without contract addr": {
			index: 2,
			expected: `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": "0x0",
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x0", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x0", "steps":"0x1f"}
				}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			txHash := block0.Transactions[test.index].Hash()
			mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[test.index], nil)
			mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[test.index], block0.Hash, block0.Number, nil)
			mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

			checkTxReceipt(t, txHash, test.expected)
		})
	}

	t.Run("pending receipt", func(t *testing.T) {
		i := 2
		expected := `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": "0x0",
					"finality_status": "ACCEPTED_ON_L2",
					"execution_status": "SUCCEEDED",
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x0", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x0", "steps":"0x1f"}
				}`

		txHash := block0.Transactions[i].Hash()
		mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[i], nil)
		mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[i], nil, uint64(0), nil)

		checkTxReceipt(t, txHash, expected)
	})

	t.Run("accepted on l1 receipt", func(t *testing.T) {
		i := 2
		expected := `{
					"type": "INVOKE",
					"transaction_hash": "0xce54bbc5647e1c1ea4276c01a708523f740db0ff5474c77734f73beec2624",
					"actual_fee": "0x0",
					"finality_status": "ACCEPTED_ON_L1",
					"execution_status": "SUCCEEDED",
					"block_hash": "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
					"block_number": 0,
					"messages_sent": [
						{
							"from_address": "0x20cfa74ee3564b4cd5435cdace0f9c4d43b939620e4a0bb5076105df0a626c6",
							"to_address": "0xc84dd7fd43a7defb5b7a15c4fbbe11cbba6db1ba",
							"payload": [
								"0xc",
								"0x22"
							]
						}
					],
					"events": [],
					"execution_resources":{"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x0", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x0", "steps":"0x1f"}
				}`

		txHash := block0.Transactions[i].Hash()
		mockReader.EXPECT().TransactionByHash(txHash).Return(block0.Transactions[i], nil)
		mockReader.EXPECT().Receipt(txHash).Return(block0.Receipts[i], block0.Hash, block0.Number, nil)
		mockReader.EXPECT().L1Head().Return(&core.L1Head{
			BlockNumber: block0.Number,
			BlockHash:   block0.Hash,
			StateRoot:   block0.GlobalStateRoot,
		}, nil)

		checkTxReceipt(t, txHash, expected)
	})
	t.Run("reverted", func(t *testing.T) {
		expected := `{
			"type": "INVOKE",
			"transaction_hash": "0x19abec18bbacec23c2eee160c70190a48e4b41dd5ff98ad8f247f9393559998",
			"actual_fee": "0x247aff6e224",
			"execution_status": "REVERTED",
			"finality_status": "ACCEPTED_ON_L2",
			"block_hash": "0x76e0229fd0c36dda2ee7905f7e4c9b3ebb78d98c4bfab550bcb3a03bf859a6",
			"block_number": 304740,
			"messages_sent": [],
			"events": [],
			"revert_reason": "Error in the called contract (0x00b1461de04c6a1aa3375bdf9b7723a8779c082ffe21311d683a0b15c078b5dc):\nError at pc=0:25:\nGot an exception while executing a hint.\nCairo traceback (most recent call last):\nUnknown location (pc=0:731)\nUnknown location (pc=0:677)\nUnknown location (pc=0:291)\nUnknown location (pc=0:314)\n\nError in the called contract (0x049d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7):\nError at pc=0:104:\nGot an exception while executing a hint.\nCairo traceback (most recent call last):\nUnknown location (pc=0:1678)\nUnknown location (pc=0:1664)\n\nError in the called contract (0x049d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7):\nError at pc=0:6:\nGot an exception while executing a hint: Assertion failed, 0 % 0x800000000000011000000000000000000000000000000000000000000000001 is equal to 0\nCairo traceback (most recent call last):\nUnknown location (pc=0:1238)\nUnknown location (pc=0:1215)\nUnknown location (pc=0:836)\n",
			"execution_resources":{"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x0", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x0","steps":"0x0"}
		}`

		integClient := feeder.NewTestClient(t, utils.Integration)
		integGw := adaptfeeder.New(integClient)

		blockWithRevertedTxn, err := integGw.BlockByNumber(context.Background(), 304740)
		require.NoError(t, err)

		revertedTxnIdx := 1
		revertedTxnHash := blockWithRevertedTxn.Transactions[revertedTxnIdx].Hash()

		mockReader.EXPECT().TransactionByHash(revertedTxnHash).Return(blockWithRevertedTxn.Transactions[revertedTxnIdx], nil)
		mockReader.EXPECT().Receipt(revertedTxnHash).Return(blockWithRevertedTxn.Receipts[revertedTxnIdx],
			blockWithRevertedTxn.Hash, blockWithRevertedTxn.Number, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		checkTxReceipt(t, revertedTxnHash, expected)
	})

	t.Run("v3 tx", func(t *testing.T) {
		expected := `{
			"block_hash": "0x50e864db6b81ce69fbeb70e6a7284ee2febbb9a2e707415de7adab83525e9cd",
			"block_number": 319132,
			"execution_status": "SUCCEEDED",
			"finality_status": "ACCEPTED_ON_L2",
			"transaction_hash": "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
			"messages_sent": [],
			"events": [
				{
					"from_address": "0x4718f5a0fc34cc1af16a1cdee98ffb20c31f5cd61d6ab07201858f4287c938d",
					"keys": [
						"0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"
					],
					"data": [
						"0x3f6f3bc663aedc5285d6013cc3ffcbc4341d86ab488b8b68d297f8258793c41",
						"0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8",
						"0x16d8b4ad4000",
						"0x0"
					]
				},
				{
					"from_address": "0x4718f5a0fc34cc1af16a1cdee98ffb20c31f5cd61d6ab07201858f4287c938d",
					"keys": [
						"0xa9fa878c35cd3d0191318f89033ca3e5501a3d90e21e3cc9256bdd5cd17fdd"
					],
					"data": [
						"0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8",
						"0x18ad8494375bc00",
						"0x0",
						"0x18aef21f822fc00",
						"0x0"
					]
				}
			],
			"execution_resources": {"bitwise_builtin_applications":"0x0", "ec_op_builtin_applications":"0x0", "ecdsa_builtin_applications":"0x0", "keccak_builtin_applications":"0x0", "memory_holes":"0x4", "pedersen_builtin_applications":"0x0", "poseidon_builtin_applications":"0x0", "range_check_builtin_applications":"0x13", "steps":"0x267"},
			"actual_fee": "0x16d8b4ad4000",
			"type": "INVOKE"
		}`

		integClient := feeder.NewTestClient(t, utils.Integration)
		integGw := adaptfeeder.New(integClient)

		block, err := integGw.BlockByNumber(context.Background(), 319132)
		require.NoError(t, err)

		index := 0
		txnHash := block.Transactions[index].Hash()

		mockReader.EXPECT().TransactionByHash(txnHash).Return(block.Transactions[index], nil)
		mockReader.EXPECT().Receipt(txnHash).Return(block.Receipts[index],
			block.Hash, block.Number, nil)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound)

		checkTxReceipt(t, txnHash, expected)
	})
}

func TestStateUpdate(t *testing.T) {
	errTests := map[string]rpc.BlockID{
		"latest":  {Latest: true},
		"pending": {Pending: true},
		"hash":    {Hash: new(felt.Felt).SetUint64(1)},
		"number":  {Number: 1},
	}

	for description, id := range errTests {
		t.Run(description, func(t *testing.T) {
			log := utils.NewNopZapLogger()
			network := utils.Mainnet
			chain := blockchain.New(pebble.NewMemTest(t), network, log)
			handler := rpc.New(chain, nil, network, nil, nil, nil, "", log)

			update, rpcErr := handler.StateUpdate(id)
			assert.Nil(t, update)
			assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
		})
	}

	mockCtrl := gomock.NewController(t)
	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", nil)

	client := feeder.NewTestClient(t, utils.Mainnet)
	mainnetGw := adaptfeeder.New(client)

	update21656, err := mainnetGw.StateUpdate(context.Background(), 21656)
	require.NoError(t, err)

	checkUpdate := func(t *testing.T, coreUpdate *core.StateUpdate, rpcUpdate *rpc.StateUpdate) {
		t.Helper()
		require.Equal(t, coreUpdate.BlockHash, rpcUpdate.BlockHash)
		require.Equal(t, coreUpdate.NewRoot, rpcUpdate.NewRoot)
		require.Equal(t, coreUpdate.OldRoot, rpcUpdate.OldRoot)

		require.Equal(t, len(coreUpdate.StateDiff.StorageDiffs), len(rpcUpdate.StateDiff.StorageDiffs))
		for _, diff := range rpcUpdate.StateDiff.StorageDiffs {
			coreDiffs := coreUpdate.StateDiff.StorageDiffs[diff.Address]
			require.Equal(t, len(coreDiffs), len(diff.StorageEntries))
			for _, entry := range diff.StorageEntries {
				require.Equal(t, *coreDiffs[entry.Key], entry.Value)
			}
		}

		require.Equal(t, len(coreUpdate.StateDiff.Nonces), len(rpcUpdate.StateDiff.Nonces))
		for _, nonce := range rpcUpdate.StateDiff.Nonces {
			require.Equal(t, *coreUpdate.StateDiff.Nonces[nonce.ContractAddress], nonce.Nonce)
		}

		require.Equal(t, len(coreUpdate.StateDiff.DeployedContracts), len(rpcUpdate.StateDiff.DeployedContracts))
		for _, deployedContract := range rpcUpdate.StateDiff.DeployedContracts {
			require.Equal(t, *coreUpdate.StateDiff.DeployedContracts[deployedContract.Address], deployedContract.ClassHash)
		}

		require.Equal(t, coreUpdate.StateDiff.DeclaredV0Classes, rpcUpdate.StateDiff.DeprecatedDeclaredClasses)

		require.Equal(t, len(coreUpdate.StateDiff.ReplacedClasses), len(rpcUpdate.StateDiff.ReplacedClasses))
		for index := range rpcUpdate.StateDiff.ReplacedClasses {
			require.Equal(t, *coreUpdate.StateDiff.ReplacedClasses[rpcUpdate.StateDiff.ReplacedClasses[index].ContractAddress],
				rpcUpdate.StateDiff.ReplacedClasses[index].ClassHash)
		}

		require.Equal(t, len(coreUpdate.StateDiff.DeclaredV1Classes), len(rpcUpdate.StateDiff.DeclaredClasses))
		for index := range rpcUpdate.StateDiff.DeclaredClasses {
			require.Equal(t, *coreUpdate.StateDiff.DeclaredV1Classes[rpcUpdate.StateDiff.DeclaredClasses[index].ClassHash],
				rpcUpdate.StateDiff.DeclaredClasses[index].CompiledClassHash)
		}
	}

	t.Run("latest", func(t *testing.T) {
		mockReader.EXPECT().Height().Return(uint64(21656), nil)
		mockReader.EXPECT().StateUpdateByNumber(uint64(21656)).Return(update21656, nil)

		update, rpcErr := handler.StateUpdate(rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)
		checkUpdate(t, update21656, update)
	})

	t.Run("by height", func(t *testing.T) {
		mockReader.EXPECT().StateUpdateByNumber(uint64(21656)).Return(update21656, nil)

		update, rpcErr := handler.StateUpdate(rpc.BlockID{Number: uint64(21656)})
		require.Nil(t, rpcErr)
		checkUpdate(t, update21656, update)
	})

	t.Run("by hash", func(t *testing.T) {
		mockReader.EXPECT().StateUpdateByHash(update21656.BlockHash).Return(update21656, nil)

		update, rpcErr := handler.StateUpdate(rpc.BlockID{Hash: update21656.BlockHash})
		require.Nil(t, rpcErr)
		checkUpdate(t, update21656, update)
	})

	t.Run("post v0.11.0", func(t *testing.T) {
		integrationClient := feeder.NewTestClient(t, utils.Integration)
		integGw := adaptfeeder.New(integrationClient)

		for name, height := range map[string]uint64{
			"declared Cairo0 classes": 283746,
			"declared Cairo1 classes": 283364,
			"replaced classes":        283428,
		} {
			t.Run(name, func(t *testing.T) {
				gwUpdate, err := integGw.StateUpdate(context.Background(), height)
				require.NoError(t, err)

				mockReader.EXPECT().StateUpdateByNumber(height).Return(gwUpdate, nil)

				update, rpcErr := handler.StateUpdate(rpc.BlockID{Number: height})
				require.Nil(t, rpcErr)

				checkUpdate(t, gwUpdate, update)
			})
		}
	})

	t.Run("pending", func(t *testing.T) {
		update21656.BlockHash = nil
		update21656.NewRoot = nil
		mockReader.EXPECT().Pending().Return(blockchain.Pending{
			StateUpdate: update21656,
		}, nil)

		update, rpcErr := handler.StateUpdate(rpc.BlockID{Pending: true})
		require.Nil(t, rpcErr)
		checkUpdate(t, update21656, update)
	})
}

func TestSyncing(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	synchronizer := mocks.NewMockSyncReader(mockCtrl)
	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, synchronizer, utils.Mainnet, nil, nil, nil, "", nil)
	defaultSyncState := false

	startingBlock := uint64(0)
	synchronizer.EXPECT().StartingBlockNumber().Return(startingBlock, errors.New("nope"))
	t.Run("undefined starting block", func(t *testing.T) {
		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, &rpc.Sync{Syncing: &defaultSyncState}, syncing)
	})

	synchronizer.EXPECT().StartingBlockNumber().Return(startingBlock, nil).AnyTimes()
	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(startingBlock).Return(nil, errors.New("empty blockchain"))

		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, &rpc.Sync{Syncing: &defaultSyncState}, syncing)
	})

	synchronizer.EXPECT().HighestBlockHeader().Return(nil).Times(2)
	t.Run("undefined highest block", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(startingBlock).Return(&core.Header{}, nil)
		mockReader.EXPECT().HeadsHeader().Return(&core.Header{}, nil)

		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, &rpc.Sync{Syncing: &defaultSyncState}, syncing)
	})
	t.Run("block height is greater than highest block", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(startingBlock).Return(&core.Header{}, nil)
		mockReader.EXPECT().HeadsHeader().Return(&core.Header{Number: 1}, nil)

		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, &rpc.Sync{Syncing: &defaultSyncState}, syncing)
	})

	synchronizer.EXPECT().HighestBlockHeader().Return(&core.Header{Number: 2, Hash: new(felt.Felt).SetUint64(2)}).Times(2)
	t.Run("block height is equal to highest block", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(startingBlock).Return(&core.Header{}, nil)
		mockReader.EXPECT().HeadsHeader().Return(&core.Header{Number: 2}, nil)

		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, &rpc.Sync{Syncing: &defaultSyncState}, syncing)
	})
	t.Run("syncing", func(t *testing.T) {
		mockReader.EXPECT().BlockHeaderByNumber(startingBlock).Return(&core.Header{Hash: &felt.Zero}, nil)
		mockReader.EXPECT().HeadsHeader().Return(&core.Header{Number: 1, Hash: new(felt.Felt).SetUint64(1)}, nil)

		currentBlockNumber := uint64(1)
		highestBlockNumber := uint64(2)
		expectedSyncing := &rpc.Sync{
			StartingBlockHash:   &felt.Zero,
			StartingBlockNumber: &startingBlock,
			CurrentBlockHash:    new(felt.Felt).SetUint64(1),
			CurrentBlockNumber:  &currentBlockNumber,
			HighestBlockHash:    new(felt.Felt).SetUint64(2),
			HighestBlockNumber:  &highestBlockNumber,
		}
		syncing, err := handler.Syncing()
		assert.Nil(t, err)
		assert.Equal(t, expectedSyncing, syncing)
	})
}

func TestNonce(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", log)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(nil, nil, errors.New("empty blockchain"))

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, nonce)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(nil, nil, errors.New("non-existent block hash"))

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Hash: &felt.Zero}, felt.Zero)
		require.Nil(t, nonce)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(nil, nil, errors.New("non-existent block number"))

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Number: 0}, felt.Zero)
		require.Nil(t, nonce)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	t.Run("non-existent contract", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractNonce(&felt.Zero).Return(nil, errors.New("non-existent contract"))

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, nonce)
		assert.Equal(t, rpc.ErrContractNotFound, rpcErr)
	})

	expectedNonce := new(felt.Felt).SetUint64(1)

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractNonce(&felt.Zero).Return(expectedNonce, nil)

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedNonce, nonce)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractNonce(&felt.Zero).Return(expectedNonce, nil)

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Hash: &felt.Zero}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedNonce, nonce)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractNonce(&felt.Zero).Return(expectedNonce, nil)

		nonce, rpcErr := handler.Nonce(rpc.BlockID{Number: 0}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedNonce, nonce)
	})
}

func TestStorageAt(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", log)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(nil, nil, errors.New("empty blockchain"))

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Latest: true})
		require.Nil(t, storage)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(nil, nil, errors.New("non-existent block hash"))

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Hash: &felt.Zero})
		require.Nil(t, storage)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(nil, nil, errors.New("non-existent block number"))

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Number: 0})
		require.Nil(t, storage)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	t.Run("non-existent contract", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractStorage(gomock.Any(), gomock.Any()).Return(nil, errors.New("non-existent contract"))

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Latest: true})
		require.Nil(t, storage)
		assert.Equal(t, rpc.ErrContractNotFound, rpcErr)
	})

	t.Run("non-existent key", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractStorage(gomock.Any(), gomock.Any()).Return(&felt.Zero, errors.New("non-existent key"))

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Latest: true})
		require.Nil(t, storage)
		assert.Equal(t, rpc.ErrContractNotFound, rpcErr)
	})

	expectedStorage := new(felt.Felt).SetUint64(1)

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractStorage(gomock.Any(), gomock.Any()).Return(expectedStorage, nil)

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Latest: true})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedStorage, storage)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractStorage(gomock.Any(), gomock.Any()).Return(expectedStorage, nil)

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Hash: &felt.Zero})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedStorage, storage)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractStorage(gomock.Any(), gomock.Any()).Return(expectedStorage, nil)

		storage, rpcErr := handler.StorageAt(felt.Zero, felt.Zero, rpc.BlockID{Number: 0})
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedStorage, storage)
	})
}

func TestClassHashAt(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", log)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(nil, nil, errors.New("empty blockchain"))

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, classHash)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(nil, nil, errors.New("non-existent block hash"))

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Hash: &felt.Zero}, felt.Zero)
		require.Nil(t, classHash)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(nil, nil, errors.New("non-existent block number"))

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Number: 0}, felt.Zero)
		require.Nil(t, classHash)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	t.Run("non-existent contract", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractClassHash(gomock.Any()).Return(nil, errors.New("non-existent contract"))

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, classHash)
		assert.Equal(t, rpc.ErrContractNotFound, rpcErr)
	})

	expectedClassHash := new(felt.Felt).SetUint64(3)

	t.Run("blockID - latest", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractClassHash(gomock.Any()).Return(expectedClassHash, nil)

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Latest: true}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedClassHash, classHash)
	})

	t.Run("blockID - hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractClassHash(gomock.Any()).Return(expectedClassHash, nil)

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Hash: &felt.Zero}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedClassHash, classHash)
	})

	t.Run("blockID - number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(mockState, nopCloser, nil)
		mockState.EXPECT().ContractClassHash(gomock.Any()).Return(expectedClassHash, nil)

		classHash, rpcErr := handler.ClassHashAt(rpc.BlockID{Number: 0}, felt.Zero)
		require.Nil(t, rpcErr)
		assert.Equal(t, expectedClassHash, classHash)
	})
}

func assertEqualCairo0Class(t *testing.T, cairo0Class *core.Cairo0Class, class *rpc.Class) {
	assert.Equal(t, cairo0Class.Program, class.Program)
	assert.Equal(t, cairo0Class.Abi, class.Abi.(json.RawMessage))

	require.Equal(t, len(cairo0Class.L1Handlers), len(class.EntryPoints.L1Handler))
	for idx := range cairo0Class.L1Handlers {
		assert.Nil(t, class.EntryPoints.L1Handler[idx].Index)
		assert.Equal(t, cairo0Class.L1Handlers[idx].Offset, class.EntryPoints.L1Handler[idx].Offset)
		assert.Equal(t, cairo0Class.L1Handlers[idx].Selector, class.EntryPoints.L1Handler[idx].Selector)
	}

	require.Equal(t, len(cairo0Class.Constructors), len(class.EntryPoints.Constructor))
	for idx := range cairo0Class.Constructors {
		assert.Nil(t, class.EntryPoints.Constructor[idx].Index)
		assert.Equal(t, cairo0Class.Constructors[idx].Offset, class.EntryPoints.Constructor[idx].Offset)
		assert.Equal(t, cairo0Class.Constructors[idx].Selector, class.EntryPoints.Constructor[idx].Selector)
	}

	require.Equal(t, len(cairo0Class.Externals), len(class.EntryPoints.External))
	for idx := range cairo0Class.Externals {
		assert.Nil(t, class.EntryPoints.External[idx].Index)
		assert.Equal(t, cairo0Class.Externals[idx].Offset, class.EntryPoints.External[idx].Offset)
		assert.Equal(t, cairo0Class.Externals[idx].Selector, class.EntryPoints.External[idx].Selector)
	}
}

func assertEqualCairo1Class(t *testing.T, cairo1Class *core.Cairo1Class, class *rpc.Class) {
	assert.Equal(t, cairo1Class.Program, class.SierraProgram)
	assert.Equal(t, cairo1Class.Abi, class.Abi.(string))
	assert.Equal(t, cairo1Class.SemanticVersion, class.ContractClassVersion)

	require.Equal(t, len(cairo1Class.EntryPoints.L1Handler), len(class.EntryPoints.L1Handler))
	for idx := range cairo1Class.EntryPoints.L1Handler {
		assert.Nil(t, class.EntryPoints.L1Handler[idx].Offset)
		assert.Equal(t, cairo1Class.EntryPoints.L1Handler[idx].Index, *class.EntryPoints.L1Handler[idx].Index)
		assert.Equal(t, cairo1Class.EntryPoints.L1Handler[idx].Selector, class.EntryPoints.L1Handler[idx].Selector)
	}

	require.Equal(t, len(cairo1Class.EntryPoints.Constructor), len(class.EntryPoints.Constructor))
	for idx := range cairo1Class.EntryPoints.Constructor {
		assert.Nil(t, class.EntryPoints.Constructor[idx].Offset)
		assert.Equal(t, cairo1Class.EntryPoints.Constructor[idx].Index, *class.EntryPoints.Constructor[idx].Index)
		assert.Equal(t, cairo1Class.EntryPoints.Constructor[idx].Selector, class.EntryPoints.Constructor[idx].Selector)
	}

	require.Equal(t, len(cairo1Class.EntryPoints.External), len(class.EntryPoints.External))
	for idx := range cairo1Class.EntryPoints.External {
		assert.Nil(t, class.EntryPoints.External[idx].Offset)
		assert.Equal(t, cairo1Class.EntryPoints.External[idx].Index, *class.EntryPoints.External[idx].Index)
		assert.Equal(t, cairo1Class.EntryPoints.External[idx].Selector, class.EntryPoints.External[idx].Selector)
	}
}

func TestClass(t *testing.T) {
	integrationClient := feeder.NewTestClient(t, utils.Integration)
	integGw := adaptfeeder.New(integrationClient)

	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	mockState.EXPECT().Class(gomock.Any()).DoAndReturn(func(classHash *felt.Felt) (*core.DeclaredClass, error) {
		class, err := integGw.Class(context.Background(), classHash)
		return &core.DeclaredClass{Class: class, At: 0}, err
	}).AnyTimes()
	mockReader.EXPECT().HeadState().Return(mockState, func() error {
		return nil
	}, nil).AnyTimes()
	mockReader.EXPECT().HeadsHeader().Return(new(core.Header), nil).AnyTimes()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", utils.NewNopZapLogger())

	latest := rpc.BlockID{Latest: true}

	t.Run("sierra class", func(t *testing.T) {
		hash := utils.HexToFelt(t, "0x1cd2edfb485241c4403254d550de0a097fa76743cd30696f714a491a454bad5")

		coreClass, err := integGw.Class(context.Background(), hash)
		require.NoError(t, err)

		class, rpcErr := handler.Class(latest, *hash)
		require.Nil(t, rpcErr)
		cairo1Class := coreClass.(*core.Cairo1Class)
		assertEqualCairo1Class(t, cairo1Class, class)
	})

	t.Run("casm class", func(t *testing.T) {
		hash := utils.HexToFelt(t, "0x4631b6b3fa31e140524b7d21ba784cea223e618bffe60b5bbdca44a8b45be04")

		coreClass, err := integGw.Class(context.Background(), hash)
		require.NoError(t, err)

		class, rpcErr := handler.Class(latest, *hash)
		require.Nil(t, rpcErr)

		cairo0Class := coreClass.(*core.Cairo0Class)
		assertEqualCairo0Class(t, cairo0Class, class)
	})
}

func TestClassAt(t *testing.T) {
	integrationClient := feeder.NewTestClient(t, utils.Integration)
	integGw := adaptfeeder.New(integrationClient)

	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	cairo0ContractAddress, _ := new(felt.Felt).SetRandom()
	cairo0ClassHash := utils.HexToFelt(t, "0x4631b6b3fa31e140524b7d21ba784cea223e618bffe60b5bbdca44a8b45be04")
	mockState.EXPECT().ContractClassHash(cairo0ContractAddress).Return(cairo0ClassHash, nil)

	cairo1ContractAddress, _ := new(felt.Felt).SetRandom()
	cairo1ClassHash := utils.HexToFelt(t, "0x1cd2edfb485241c4403254d550de0a097fa76743cd30696f714a491a454bad5")
	mockState.EXPECT().ContractClassHash(cairo1ContractAddress).Return(cairo1ClassHash, nil)

	mockState.EXPECT().Class(gomock.Any()).DoAndReturn(func(classHash *felt.Felt) (*core.DeclaredClass, error) {
		class, err := integGw.Class(context.Background(), classHash)
		return &core.DeclaredClass{Class: class, At: 0}, err
	}).AnyTimes()
	mockReader.EXPECT().HeadState().Return(mockState, func() error {
		return nil
	}, nil).AnyTimes()
	mockReader.EXPECT().HeadsHeader().Return(new(core.Header), nil).AnyTimes()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", utils.NewNopZapLogger())

	latest := rpc.BlockID{Latest: true}

	t.Run("sierra class", func(t *testing.T) {
		coreClass, err := integGw.Class(context.Background(), cairo1ClassHash)
		require.NoError(t, err)

		class, rpcErr := handler.ClassAt(latest, *cairo1ContractAddress)
		require.Nil(t, rpcErr)
		cairo1Class := coreClass.(*core.Cairo1Class)
		assertEqualCairo1Class(t, cairo1Class, class)
	})

	t.Run("casm class", func(t *testing.T) {
		coreClass, err := integGw.Class(context.Background(), cairo0ClassHash)
		require.NoError(t, err)

		class, rpcErr := handler.ClassAt(latest, *cairo0ContractAddress)
		require.Nil(t, rpcErr)

		cairo0Class := coreClass.(*core.Cairo0Class)
		assertEqualCairo0Class(t, cairo0Class, class)
	})
}

func TestEvents(t *testing.T) {
	testDB := pebble.NewMemTest(t)
	chain := blockchain.New(testDB, utils.Goerli2, utils.NewNopZapLogger())

	client := feeder.NewTestClient(t, utils.Goerli2)
	gw := adaptfeeder.New(client)

	for i := 0; i < 7; i++ {
		b, err := gw.BlockByNumber(context.Background(), uint64(i))
		require.NoError(t, err)
		s, err := gw.StateUpdate(context.Background(), uint64(i))
		require.NoError(t, err)

		if b.Number < 6 {
			require.NoError(t, chain.Store(b, &core.BlockCommitments{}, s, nil))
		} else {
			b.Hash = nil
			b.GlobalStateRoot = nil
			require.NoError(t, chain.StorePending(&blockchain.Pending{
				Block:       b,
				StateUpdate: s,
			}))
		}
	}

	handler := rpc.New(chain, nil, utils.Goerli2, nil, nil, nil, "", utils.NewNopZapLogger())
	from := utils.HexToFelt(t, "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7")
	args := rpc.EventsArg{
		EventFilter: rpc.EventFilter{
			FromBlock: &rpc.BlockID{Number: 0},
			ToBlock:   &rpc.BlockID{Latest: true},
			Address:   from,
			Keys:      [][]felt.Felt{},
		},
		ResultPageRequest: rpc.ResultPageRequest{
			ChunkSize:         100,
			ContinuationToken: "",
		},
	}

	t.Run("filter non-existent", func(t *testing.T) {
		t.Run("block number", func(t *testing.T) {
			args.ToBlock = &rpc.BlockID{Number: 55}
			events, err := handler.Events(args)
			require.Nil(t, err)
			require.Len(t, events.Events, 3)
		})

		t.Run("block hash", func(t *testing.T) {
			args.ToBlock = &rpc.BlockID{Hash: new(felt.Felt).SetUint64(55)}
			_, err := handler.Events(args)
			require.Equal(t, rpc.ErrBlockNotFound, err)
		})
	})

	t.Run("filter with no from_block", func(t *testing.T) {
		args.FromBlock = nil
		args.ToBlock = &rpc.BlockID{Latest: true}
		_, err := handler.Events(args)
		require.Nil(t, err)
	})

	t.Run("filter with no to_block", func(t *testing.T) {
		args.FromBlock = &rpc.BlockID{Number: 0}
		args.ToBlock = nil
		_, err := handler.Events(args)
		require.Nil(t, err)
	})

	t.Run("filter with no address", func(t *testing.T) {
		args.ToBlock = &rpc.BlockID{Latest: true}
		args.Address = nil
		_, err := handler.Events(args)
		require.Nil(t, err)
	})

	t.Run("filter with no keys", func(t *testing.T) {
		var allEvents []*rpc.EmittedEvent
		t.Run("get canonical events without pagination", func(t *testing.T) {
			args.ToBlock = &rpc.BlockID{Latest: true}
			args.Address = from
			events, err := handler.Events(args)
			require.Nil(t, err)
			require.Len(t, events.Events, 2)
			require.Empty(t, events.ContinuationToken)
			allEvents = events.Events
		})

		t.Run("accumulate events with pagination", func(t *testing.T) {
			var accEvents []*rpc.EmittedEvent
			args.ChunkSize = 1

			for i := 0; i < len(allEvents)+1; i++ {
				events, err := handler.Events(args)
				require.Nil(t, err)
				accEvents = append(accEvents, events.Events...)
				args.ContinuationToken = events.ContinuationToken
				if args.ContinuationToken == "" {
					break
				}
			}
			require.Equal(t, allEvents, accEvents)
		})
	})

	t.Run("filter with keys", func(t *testing.T) {
		key := utils.HexToFelt(t, "0x3774b0545aabb37c45c1eddc6a7dae57de498aae6d5e3589e362d4b4323a533")

		t.Run("get all events without pagination", func(t *testing.T) {
			args.ChunkSize = 100
			args.Keys = append(args.Keys, []felt.Felt{*key})
			events, err := handler.Events(args)
			require.Nil(t, err)
			require.Len(t, events.Events, 1)
			require.Empty(t, events.ContinuationToken)

			require.Equal(t, from, events.Events[0].From)
			require.Equal(t, []*felt.Felt{key}, events.Events[0].Keys)
			require.Equal(t, []*felt.Felt{
				utils.HexToFelt(t, "0x2ee9bf3da86f3715e8a20429feed8e37fef58004ee5cf52baf2d8fc0d94c9c8"),
				utils.HexToFelt(t, "0x2ee9bf3da86f3715e8a20429feed8e37fef58004ee5cf52baf2d8fc0d94c9c8"),
			}, events.Events[0].Data)
			require.Equal(t, uint64(5), *events.Events[0].BlockNumber)
			require.Equal(t, utils.HexToFelt(t, "0x3b43b334f46b921938854ba85ffc890c1b1321f8fd69e7b2961b18b4260de14"), events.Events[0].BlockHash)
			require.Equal(t, utils.HexToFelt(t, "0x6d1431d875ba082365b888c1651e026012a94172b04589c91c2adeb6c1b7ace"), events.Events[0].TransactionHash)
		})
	})

	t.Run("large page size", func(t *testing.T) {
		args.ChunkSize = 10240 + 1
		events, err := handler.Events(args)
		require.Equal(t, rpc.ErrPageSizeTooBig, err)
		require.Nil(t, events)
	})

	t.Run("too many keys", func(t *testing.T) {
		args.ChunkSize = 2
		args.Keys = make([][]felt.Felt, 1024+1)
		events, err := handler.Events(args)
		require.Equal(t, rpc.ErrTooManyKeysInFilter, err)
		require.Nil(t, events)
	})

	t.Run("filter with limit", func(t *testing.T) {
		handler = handler.WithFilterLimit(1)
		key := utils.HexToFelt(t, "0x3774b0545aabb37c45c1eddc6a7dae57de498aae6d5e3589e362d4b4323a533")
		args.ChunkSize = 100
		args.Keys = make([][]felt.Felt, 0)
		args.Keys = append(args.Keys, []felt.Felt{*key})
		events, err := handler.Events(args)
		require.Nil(t, err)
		require.Equal(t, "1-0", events.ContinuationToken)
		require.Empty(t, events.Events)
		handler = handler.WithFilterLimit(7)
		events, err = handler.Events(args)
		require.Nil(t, err)
		require.Empty(t, events.ContinuationToken)
		require.NotEmpty(t, events.Events)
	})

	t.Run("get pending events without pagination", func(t *testing.T) {
		args = rpc.EventsArg{
			EventFilter: rpc.EventFilter{
				FromBlock: &rpc.BlockID{Pending: true},
				ToBlock:   &rpc.BlockID{Pending: true},
			},
			ResultPageRequest: rpc.ResultPageRequest{
				ChunkSize:         100,
				ContinuationToken: "",
			},
		}
		events, err := handler.Events(args)
		require.Nil(t, err)
		require.Len(t, events.Events, 1)
		require.Empty(t, events.ContinuationToken)

		assert.Nil(t, events.Events[0].BlockHash)
		assert.Nil(t, events.Events[0].BlockNumber)
		assert.Equal(t, utils.HexToFelt(t, "0x5fe34d6903420e489b6faa8804c7a1af311446934bac1ba1e79b53cee61756c"), events.Events[0].TransactionHash)
	})
}

func TestAddTransactionUnmarshal(t *testing.T) {
	tests := map[string]string{
		"deploy account v3": `{
			"type": "DEPLOY_ACCOUNT",
			"version": "0x3",
			"signature": [
				"0x73c0e0fe22d6e82187b84e06f33644f7dc6edce494a317bfcdd0bb57ab862fa",
				"0x6119aa7d091eac96f07d7d195f12eff9a8786af85ddf41028428ee8f510e75e"
			],
			"nonce": "0x0",
			"contract_address_salt": "0x510b540d51c06e1539cbc42e93a37cbef534082c75a3991179cfac83da67fdb",
			"constructor_calldata": [
				"0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2",
				"0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463",
				"0x2",
				"0x510b540d51c06e1539cbc42e93a37cbef534082c75a3991179cfac83da67fdb",
				"0x0"
			],
			"class_hash": "0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918",
			"resource_bounds": {
				"l1_gas": {
					"max_amount": "0x6fde2b4eb000",
					"max_price_per_unit": "0x6fde2b4eb000"
				},
				"l2_gas": {
					"max_amount": "0x6fde2b4eb000",
					"max_price_per_unit": "0x6fde2b4eb000"
				}
			},
			"tip": "0x0",
			"paymaster_data": [],
			"nonce_data_availability_mode": "L1",
			"fee_data_availability_mode": "L2"
		}`,
	}

	for description, txJSON := range tests {
		t.Run(description, func(t *testing.T) {
			tx := rpc.BroadcastedTransaction{}
			require.NoError(t, json.Unmarshal([]byte(txJSON), &tx))
		})
	}
}

func TestAddTransaction(t *testing.T) {
	network := utils.Integration
	gw := adaptfeeder.New(feeder.NewTestClient(t, network))
	txWithoutClass := func(hash string) rpc.BroadcastedTransaction {
		tx, err := gw.Transaction(context.Background(), utils.HexToFelt(t, hash))
		require.NoError(t, err)
		return rpc.BroadcastedTransaction{
			Transaction: *rpc.AdaptTransaction(tx),
		}
	}
	tests := map[string]struct {
		txn          rpc.BroadcastedTransaction
		expectedJSON string
	}{
		"invoke v0": {
			txn: txWithoutClass("0x5e91283c1c04c3f88e4a98070df71227fb44dea04ce349c7eb379f85a10d1c3"),
			expectedJSON: `{
				"transaction_hash": "0x5e91283c1c04c3f88e4a98070df71227fb44dea04ce349c7eb379f85a10d1c3",
				"version": "0x0",
				"max_fee": "0x0",
				"signature": [],
				"entry_point_selector": "0x218f305395474a84a39307fa5297be118fe17bf65e27ac5e2de6617baa44c64",
				"calldata": [
				  "0x79631f37538379fc32739605910733219b836b050766a2349e93ec375e62885",
				  "0x0"
				],
				"contract_address": "0x2cbc1f6e80a024900dc949914c7692f802ba90012cda39115db5640f5eca847",
				"type": "INVOKE_FUNCTION"
			  }`,
		},
		"invoke v1": {
			txn: txWithoutClass("0x45d9c2c8e01bacae6dec3438874576a4a1ce65f1d4247f4e9748f0e7216838"),
			expectedJSON: `{
				"transaction_hash": "0x45d9c2c8e01bacae6dec3438874576a4a1ce65f1d4247f4e9748f0e7216838",
				"version": "0x1",
				"max_fee": "0x2386f26fc10000",
				"signature": [
				  "0x89aa2f42e07913b6dee313c3ef680efb99892feb3e2d08287e01e63418da7a",
				  "0x458fb4c942d5407d8c1ef1557d29487ab8217842d28a907d75ee0828243361"
				],
				"nonce": "0x99d",
				"sender_address": "0x219937256cd88844f9fdc9c33a2d6d492e253ae13814c2dc0ecab7f26919d46",
				"calldata": [
				  "0x1",
				  "0x7812357541c81dd9a320c2339c0c76add710db15f8cc29e8dde8e588cad4455",
				  "0x7772be8b80a8a33dc6c1f9a6ab820c02e537c73e859de67f288c70f92571bb",
				  "0x0",
				  "0x3",
				  "0x3",
				  "0x24b037cd0ffd500467f4cc7d0b9df27abdc8646379e818e3ce3d9925fc9daec",
				  "0x4b7797c3f6a6d9b1a28bbd6645d3f009bd12587581e21011aeb9b176f801ab0",
				  "0xdfeaf5f022324453e6058c00c7d35ee449c1d01bb897ccb5df20f697d98f26"
				],
				"type": "INVOKE_FUNCTION"
			  }`,
		},
		"invoke v3": {
			txn: txWithoutClass("0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd"),
			expectedJSON: `{
				"transaction_hash": "0x49728601e0bb2f48ce506b0cbd9c0e2a9e50d95858aa41463f46386dca489fd",
				"version": "0x3",
				"signature": [
				  "0x71a9b2cd8a8a6a4ca284dcddcdefc6c4fd20b92c1b201bd9836e4ce376fad16",
				  "0x6bef4745194c9447fdc8dd3aec4fc738ab0a560b0d2c7bf62fbf58aef3abfc5"
				],
				"nonce": "0xe97",
				"nonce_data_availability_mode": 0,
				"fee_data_availability_mode": 0,
				"resource_bounds": {
				  "L1_GAS": {
					"max_amount": "0x186a0",
					"max_price_per_unit": "0x5af3107a4000"
				  },
				  "L2_GAS": {
					"max_amount": "0x0",
					"max_price_per_unit": "0x0"
				  }
				},
				"tip": "0x0",
				"paymaster_data": [],
				"sender_address": "0x3f6f3bc663aedc5285d6013cc3ffcbc4341d86ab488b8b68d297f8258793c41",
				"calldata": [
				  "0x2",
				  "0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
				  "0x27c3334165536f239cfd400ed956eabff55fc60de4fb56728b6a4f6b87db01c",
				  "0x0",
				  "0x4",
				  "0x4c312760dfd17a954cdd09e76aa9f149f806d88ec3e402ffaf5c4926f568a42",
				  "0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
				  "0x4",
				  "0x1",
				  "0x5",
				  "0x450703c32370cf7ffff540b9352e7ee4ad583af143a361155f2b485c0c39684",
				  "0x5df99ae77df976b4f0e5cf28c7dcfe09bd6e81aab787b19ac0c08e03d928cf",
				  "0x1",
				  "0x7fe4fd616c7fece1244b3616bb516562e230be8c9f29668b46ce0369d5ca829",
				  "0x287acddb27a2f9ba7f2612d72788dc96a5b30e401fc1e8072250940e024a587"
				],
				"account_deployment_data": [],
				"type": "INVOKE_FUNCTION"
			  }`,
		},
		"deploy v0": {
			txn: txWithoutClass("0x2e3106421d38175020cd23a6f1bff87989a64cae6a679c54c7710a033d88faa"),
			expectedJSON: `{
				"transaction_hash": "0x2e3106421d38175020cd23a6f1bff87989a64cae6a679c54c7710a033d88faa",
				"version": "0x0",
				"contract_address_salt": "0x5de1c0a37865820ce4896872e78da6877b0a8eede3d363131734556a8815d52",
				"class_hash": "0x71468bd837666b3a05cca1a5363b0d9e15cacafd6eeaddfbc4f00d5c7b9a51d",
				"constructor_calldata": [],
				"type": "DEPLOY"
			  }`,
		},
		"declare v1": {
			txn: txWithoutClass("0x2d667ed0aa3a8faef96b466972079826e592ec0aebefafd77a39f2ed06486b4"),
			expectedJSON: `{
				"transaction_hash": "0x2d667ed0aa3a8faef96b466972079826e592ec0aebefafd77a39f2ed06486b4",
				"version": "0x1",
				"max_fee": "0x2386f26fc10000",
				"signature": [
				  "0x17872d12092aa60331394f514de908309fdba185997fd3d0be1e2896cd1e053",
				  "0x66124ebfe1a34809b2223a9707ac796dc6f4b6310cb002bda1e4c062a4b2867"
				],
				"nonce": "0x1078",
				"class_hash": "0x772164c9d6179a89e7f1167f099219f47d752304b16ed01f081b6e0b45c93c3",
				"sender_address": "0x52125c1e043126c637d1436d9551ef6c4f6e3e36945676bbd716a56e3a41b7a",
				"type": "DECLARE"
			  }`,
		},
		"declare v2": {
			txn: func() rpc.BroadcastedTransaction {
				tx := txWithoutClass("0x44b971f7eface29b185f86dd7b3b70acb1e48e0ad459e3a41e06fc42937aaa4")
				tx.ContractClass = json.RawMessage([]byte(`{"sierra_program": {}}`))
				return tx
			}(),
			expectedJSON: `{
				"transaction_hash": "0x44b971f7eface29b185f86dd7b3b70acb1e48e0ad459e3a41e06fc42937aaa4",
				"version": "0x2",
				"max_fee": "0x50c8f30c048",
				"signature": [
				  "0x42a40a113a4381e5f304fd28a707ba4182609db42062a7f36b9291bf8ae8ae7",
				  "0x6035bcf022f887c80dbc2b615e927d662637d2213335ee657893dce8ddabe5b"
				],
				"nonce": "0x11",
				"class_hash": "0x7cb013a4139335cefce52adc2ac342c0110811353e7992baefbe547200223c7",
				"contract_class": {
					"sierra_program": "H4sIAAAAAAAA/6quBQQAAP//Q7+mowIAAAA="
				},
				"compiled_class_hash": "0x67f7deab53a3ba70500bdafe66fb3038bbbaadb36a6dd1a7a5fc5b094e9d724",
				"sender_address": "0x3bb81d22ecd0e0a6f3138bdc5c072ff5726c5add02bcfd5b81cd657a6ae10a8",
				"type": "DECLARE"
			  }`,
		},
		"declare v3": {
			txn: func() rpc.BroadcastedTransaction {
				tx := txWithoutClass("0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3")
				tx.ContractClass = json.RawMessage([]byte(`{"sierra_program": {}}`))
				return tx
			}(),
			expectedJSON: `{
				"transaction_hash": "0x41d1f5206ef58a443e7d3d1ca073171ec25fa75313394318fc83a074a6631c3",
				"version": "0x3",
				"signature": [
				  "0x29a49dff154fede73dd7b5ca5a0beadf40b4b069f3a850cd8428e54dc809ccc",
				  "0x429d142a17223b4f2acde0f5ecb9ad453e188b245003c86fab5c109bad58fc3"
				],
				"nonce": "0x1",
				"nonce_data_availability_mode": 0,
				"fee_data_availability_mode": 0,
				"resource_bounds": {
				  "L1_GAS": {
					"max_amount": "0x186a0",
					"max_price_per_unit": "0x2540be400"
				  },
				  "L2_GAS": {
					"max_amount": "0x0",
					"max_price_per_unit": "0x0"
				  }
				},
				"tip": "0x0",
				"paymaster_data": [],
				"sender_address": "0x2fab82e4aef1d8664874e1f194951856d48463c3e6bf9a8c68e234a629a6f50",
				"class_hash": "0x5ae9d09292a50ed48c5930904c880dab56e85b825022a7d689cfc9e65e01ee7",
				"compiled_class_hash": "0x1add56d64bebf8140f3b8a38bdf102b7874437f0c861ab4ca7526ec33b4d0f8",
				"account_deployment_data": [],
				"type": "DECLARE",
				"contract_class": {
					"sierra_program": "H4sIAAAAAAAA/6quBQQAAP//Q7+mowIAAAA="
				}
			  }`,
		},
		"deploy account v1": {
			txn: txWithoutClass("0x658f1c44ebf6a1540eac0680956c3a9d315f65d2cb3b53593345905fed3982a"),
			expectedJSON: `{
				"transaction_hash": "0x658f1c44ebf6a1540eac0680956c3a9d315f65d2cb3b53593345905fed3982a",
				"version": "0x1",
				"max_fee": "0x2386f273b213da",
				"signature": [
				  "0x7d31509f555031323050ed226012f0c6361b3dc34f0f5d2c65a76870fd8908b",
				  "0x58d64f6d39dfb20586da0c40e3d575cab940009cdee6423b03268fd893bd27a"
				],
				"nonce": "0x0",
				"contract_address_salt": "0x7b9f4b7d6d49b60686004dd850a4b41c818d6eb69e226b8ea37ea025e6830f5",
				"class_hash": "0x5a9941d0cc16b8619a3325055472da709a66113afcc6a8ab86055da7d29c5f8",
				"constructor_calldata": [
				  "0x7b16a9b7bb08d36950aa5d27d4d2c64bfd54f3ae16a0e01f21a6d410cb5179c"
				],
				"type": "DEPLOY_ACCOUNT"
			  }`,
		},
		"deploy account v3": {
			txn: txWithoutClass("0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0"),
			expectedJSON: `{
				"transaction_hash": "0x29fd7881f14380842414cdfdd8d6c0b1f2174f8916edcfeb1ede1eb26ac3ef0",
				"version": "0x3",
				"signature": [
				  "0x6d756e754793d828c6c1a89c13f7ec70dbd8837dfeea5028a673b80e0d6b4ec",
				  "0x4daebba599f860daee8f6e100601d98873052e1c61530c630cc4375c6bd48e3"
				],
				"nonce": "0x0",
				"nonce_data_availability_mode": 0,
				"fee_data_availability_mode": 0,
				"resource_bounds": {
				  "L1_GAS": {
					"max_amount": "0x186a0",
					"max_price_per_unit": "0x5af3107a4000"
				  },
				  "L2_GAS": {
					"max_amount": "0x0",
					"max_price_per_unit": "0x0"
				  }
				},
				"tip": "0x0",
				"paymaster_data": [],
				"contract_address_salt": "0x0",
				"class_hash": "0x2338634f11772ea342365abd5be9d9dc8a6f44f159ad782fdebd3db5d969738",
				"constructor_calldata": [
				  "0x5cd65f3d7daea6c63939d659b8473ea0c5cd81576035a4d34e52fb06840196c"
				],
				"type": "DEPLOY_ACCOUNT"
			  }`,
		},
	}

	for description, test := range tests {
		t.Run(description, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			t.Cleanup(mockCtrl.Finish)

			mockGateway := mocks.NewMockGateway(mockCtrl)
			mockGateway.
				EXPECT().
				AddTransaction(gomock.Any()).
				Do(func(txnJSON json.RawMessage) error {
					assert.JSONEq(t, test.expectedJSON, string(txnJSON), string(txnJSON))
					gatewayTx := starknet.Transaction{}
					// Ensure the Starknet transaction can be unmarshaled properly.
					require.NoError(t, json.Unmarshal(txnJSON, &gatewayTx))
					return nil
				}).
				Return(json.RawMessage(`{
					"transaction_hash": "0x1",
					"address": "0x2",
					"class_hash": "0x3"
				}`), nil).
				Times(1)

			handler := rpc.New(nil, nil, network, mockGateway, nil, nil, "", utils.NewNopZapLogger())
			got, rpcErr := handler.AddTransaction(test.txn)
			require.Nil(t, rpcErr)
			require.Equal(t, &rpc.AddTxResponse{
				TransactionHash: utils.HexToFelt(t, "0x1"),
				ContractAddress: utils.HexToFelt(t, "0x2"),
				ClassHash:       utils.HexToFelt(t, "0x3"),
			}, got)
		})
	}
}

func TestVersion(t *testing.T) {
	const version = "1.2.3-rc1"

	handler := rpc.New(nil, nil, utils.Mainnet, nil, nil, nil, version, nil)
	ver, err := handler.Version()
	require.Nil(t, err)
	assert.Equal(t, version, ver)
}

func TestTransactionStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	tests := []struct {
		network           utils.Network
		verifiedTxHash    *felt.Felt
		nonVerifiedTxHash *felt.Felt
		notFoundTxHash    *felt.Felt
	}{
		{
			network:           utils.Mainnet,
			verifiedTxHash:    utils.HexToFelt(t, "0xf1d99fb97509e0dfc425ddc2a8c5398b74231658ca58b6f8da92f39cb739e"),
			nonVerifiedTxHash: utils.HexToFelt(t, "0x6c40890743aa220b10e5ee68cef694c5c23cc2defd0dbdf5546e687f9982ab1"),
			notFoundTxHash:    utils.HexToFelt(t, "0x8c96a2b3d73294667e489bf8904c6aa7c334e38e24ad5a721c7e04439ff9"),
		},
		{
			network:           utils.Integration,
			verifiedTxHash:    utils.HexToFelt(t, "0x5e91283c1c04c3f88e4a98070df71227fb44dea04ce349c7eb379f85a10d1c3"),
			nonVerifiedTxHash: utils.HexToFelt(t, "0x45d9c2c8e01bacae6dec3438874576a4a1ce65f1d4247f4e9748f0e7216838"),
			notFoundTxHash:    utils.HexToFelt(t, "0xd7747f3d0ce84b3a19b05b987a782beac22c54e66773303e94ea78cc3c15"),
		},
	}

	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.network.String(), func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			t.Cleanup(mockCtrl.Finish)

			client := feeder.NewTestClient(t, test.network)

			t.Run("tx found in db", func(t *testing.T) {
				gw := adaptfeeder.New(client)

				block, err := gw.BlockLatest(context.Background())
				require.NoError(t, err)

				tx := block.Transactions[0]

				t.Run("not verified", func(t *testing.T) {
					mockReader := mocks.NewMockReader(mockCtrl)
					mockReader.EXPECT().TransactionByHash(tx.Hash()).Return(tx, nil)
					mockReader.EXPECT().Receipt(tx.Hash()).Return(block.Receipts[0], block.Hash, block.Number, nil)
					mockReader.EXPECT().L1Head().Return(nil, nil)

					handler := rpc.New(mockReader, nil, test.network, nil, nil, nil, "", nil)

					want := &rpc.TransactionStatus{
						Finality:  rpc.TxnStatusAcceptedOnL2,
						Execution: rpc.TxnSuccess,
					}
					status, rpcErr := handler.TransactionStatus(ctx, *tx.Hash())
					require.Nil(t, rpcErr)
					require.Equal(t, want, status)
				})
				t.Run("verified", func(t *testing.T) {
					mockReader := mocks.NewMockReader(mockCtrl)
					mockReader.EXPECT().TransactionByHash(tx.Hash()).Return(tx, nil)
					mockReader.EXPECT().Receipt(tx.Hash()).Return(block.Receipts[0], block.Hash, block.Number, nil)
					mockReader.EXPECT().L1Head().Return(&core.L1Head{
						BlockNumber: block.Number + 1,
					}, nil)

					handler := rpc.New(mockReader, nil, test.network, nil, nil, nil, "", nil)

					want := &rpc.TransactionStatus{
						Finality:  rpc.TxnStatusAcceptedOnL1,
						Execution: rpc.TxnSuccess,
					}
					status, rpcErr := handler.TransactionStatus(ctx, *tx.Hash())
					require.Nil(t, rpcErr)
					require.Equal(t, want, status)
				})
			})
			t.Run("transaction not found in db", func(t *testing.T) {
				notFoundTests := map[string]struct {
					finality rpc.TxnStatus
					hash     *felt.Felt
				}{
					"verified": {
						finality: rpc.TxnStatusAcceptedOnL1,
						hash:     test.verifiedTxHash,
					},
					"not verified": {
						finality: rpc.TxnStatusAcceptedOnL2,
						hash:     test.nonVerifiedTxHash,
					},
				}

				for description, notFoundTest := range notFoundTests {
					t.Run(description, func(t *testing.T) {
						mockReader := mocks.NewMockReader(mockCtrl)
						mockReader.EXPECT().TransactionByHash(notFoundTest.hash).Return(nil, db.ErrKeyNotFound)
						handler := rpc.New(mockReader, nil, test.network, nil, client, nil, "", nil)

						status, err := handler.TransactionStatus(ctx, *notFoundTest.hash)
						require.Nil(t, err)
						require.Equal(t, notFoundTest.finality, status.Finality)
						require.Equal(t, rpc.TxnSuccess, status.Execution)
					})
				}
			})

			// transaction no† found in db and feeder
			t.Run("transaction not found in db and feeder  ", func(t *testing.T) {
				mockReader := mocks.NewMockReader(mockCtrl)
				mockReader.EXPECT().TransactionByHash(test.notFoundTxHash).Return(nil, db.ErrKeyNotFound)
				handler := rpc.New(mockReader, nil, test.network, nil, client, nil, "", nil)

				_, err := handler.TransactionStatus(ctx, *test.notFoundTxHash)
				require.NotNil(t, err)
				require.Equal(t, err, rpc.ErrTxnHashNotFound)
			})
		})
	}
}

func TestCall(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, nil, "", log)

	t.Run("empty blockchain", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(nil, nil, errors.New("empty blockchain"))

		res, rpcErr := handler.Call(rpc.FunctionCall{}, rpc.BlockID{Latest: true})
		require.Nil(t, res)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block hash", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockHash(&felt.Zero).Return(nil, nil, errors.New("non-existent block hash"))

		res, rpcErr := handler.Call(rpc.FunctionCall{}, rpc.BlockID{Hash: &felt.Zero})
		require.Nil(t, res)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	t.Run("non-existent block number", func(t *testing.T) {
		mockReader.EXPECT().StateAtBlockNumber(uint64(0)).Return(nil, nil, errors.New("non-existent block number"))

		res, rpcErr := handler.Call(rpc.FunctionCall{}, rpc.BlockID{Number: 0})
		require.Nil(t, res)
		assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
	})

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	t.Run("call - unknown contract", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockReader.EXPECT().HeadsHeader().Return(new(core.Header), nil)
		mockState.EXPECT().ContractClassHash(&felt.Zero).Return(nil, errors.New("unknown contract"))

		res, rpcErr := handler.Call(rpc.FunctionCall{}, rpc.BlockID{Latest: true})
		require.Nil(t, res)
		assert.Equal(t, rpc.ErrContractNotFound, rpcErr)
	})
}

func TestEstimateMessageFee(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()

	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, mockVM, "", log)
	msg := rpc.MsgFromL1{
		From:     common.HexToAddress("0xDEADBEEF"),
		To:       *new(felt.Felt).SetUint64(1337),
		Payload:  []felt.Felt{*new(felt.Felt).SetUint64(1), *new(felt.Felt).SetUint64(2)},
		Selector: *new(felt.Felt).SetUint64(44),
	}

	t.Run("block not found", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(nil, nil, errors.New("not found"))
		_, err := handler.EstimateMessageFee(msg, rpc.BlockID{Latest: true})
		require.Equal(t, rpc.ErrBlockNotFound, err)
	})

	latestHeader := &core.Header{
		Number:    123,
		Timestamp: 456,
		GasPrice:  new(felt.Felt).SetUint64(42),
	}
	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
	mockReader.EXPECT().HeadsHeader().Return(latestHeader, nil)

	expectedGasConsumed := new(felt.Felt).SetUint64(37)
	mockVM.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), utils.Mainnet, gomock.Any(), gomock.Any(), gomock.Any(), true, latestHeader.GasPrice,
		latestHeader.GasPriceSTRK, false).DoAndReturn(
		func(txns []core.Transaction, declaredClasses []core.Class, blockNumber, blockTimestamp uint64,
			sequencerAddress *felt.Felt, state core.StateReader, network utils.Network, paidFeesOnL1 []*felt.Felt,
			skipChargeFee, skipValidate, errOnRevert bool, gasPriceWei, gasPriceSTRK *felt.Felt, legacyTraceJson bool,
		) ([]*felt.Felt, []json.RawMessage, error) {
			require.Len(t, txns, 1)
			assert.NotNil(t, txns[0].(*core.L1HandlerTransaction))

			assert.Empty(t, declaredClasses)
			assert.Equal(t, latestHeader.Number, blockNumber)
			assert.Equal(t, latestHeader.Timestamp, blockTimestamp)
			assert.NotNil(t, sequencerAddress)
			assert.Len(t, paidFeesOnL1, 1)

			actualFee := new(felt.Felt).Mul(expectedGasConsumed, gasPriceWei)
			return []*felt.Felt{actualFee}, []json.RawMessage{{}}, nil
		},
	)

	estimateFee, err := handler.EstimateMessageFee(msg, rpc.BlockID{Latest: true})
	require.Nil(t, err)
	feeUnit := rpc.WEI
	require.Equal(t, rpc.FeeEstimate{
		GasConsumed: expectedGasConsumed,
		GasPrice:    latestHeader.GasPrice,
		OverallFee:  new(felt.Felt).Mul(expectedGasConsumed, latestHeader.GasPrice),
		Unit:        &feeUnit,
	}, *estimateFee)
}

func TestLegacyEstimateMessageFee(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()

	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, mockVM, "", log)
	msg := rpc.MsgFromL1{
		From:     common.HexToAddress("0xDEADBEEF"),
		To:       *new(felt.Felt).SetUint64(1337),
		Payload:  []felt.Felt{*new(felt.Felt).SetUint64(1), *new(felt.Felt).SetUint64(2)},
		Selector: *new(felt.Felt).SetUint64(44),
	}

	latestHeader := &core.Header{
		Number:    123,
		Timestamp: 456,
		GasPrice:  new(felt.Felt).SetUint64(42),
	}
	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
	mockReader.EXPECT().HeadsHeader().Return(latestHeader, nil)

	expectedGasConsumed := new(felt.Felt).SetUint64(37)
	mockVM.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), utils.Mainnet, gomock.Any(), gomock.Any(), gomock.Any(), true, latestHeader.GasPrice, latestHeader.GasPriceSTRK, false).DoAndReturn(
		func(txns []core.Transaction, declaredClasses []core.Class, blockNumber, blockTimestamp uint64,
			sequencerAddress *felt.Felt, state core.StateReader, network utils.Network, paidFeesOnL1 []*felt.Felt,
			skipChargeFee, skipValidate, errOnRevert bool, gasPriceWei, gasPriceSTRK *felt.Felt, legacyTraceJson bool,
		) ([]*felt.Felt, []json.RawMessage, error) {
			actualFee := new(felt.Felt).Mul(expectedGasConsumed, gasPriceWei)
			return []*felt.Felt{actualFee}, []json.RawMessage{{}}, nil
		},
	)

	estimateFee, err := handler.LegacyEstimateMessageFee(msg, rpc.BlockID{Latest: true})
	require.Nil(t, err)
	require.Equal(t, rpc.FeeEstimate{
		GasConsumed: expectedGasConsumed,
		GasPrice:    latestHeader.GasPrice,
		OverallFee:  new(felt.Felt).Mul(expectedGasConsumed, latestHeader.GasPrice),
	}, *estimateFee)
}

func TestTraceTransaction(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, utils.Mainnet, nil, nil, mockVM, "", log)

	t.Run("not found", func(t *testing.T) {
		hash := utils.HexToFelt(t, "0xBBBB")
		// Receipt() returns error related to db
		mockReader.EXPECT().Receipt(hash).Return(nil, nil, uint64(0), db.ErrKeyNotFound)

		trace, err := handler.TraceTransaction(context.Background(), *hash)
		assert.Nil(t, trace)
		assert.Equal(t, rpc.ErrTxnHashNotFound, err)
	})
	t.Run("ok", func(t *testing.T) {
		hash := utils.HexToFelt(t, "0x37b244ea7dc6b3f9735fba02d183ef0d6807a572dd91a63cc1b14b923c1ac0")
		tx := &core.DeclareTransaction{
			TransactionHash: hash,
			ClassHash:       utils.HexToFelt(t, "0x000000000"),
		}

		header := &core.Header{
			Hash:             utils.HexToFelt(t, "0xCAFEBABE"),
			ParentHash:       utils.HexToFelt(t, "0x0"),
			Number:           0,
			SequencerAddress: utils.HexToFelt(t, "0X111"),
			ProtocolVersion:  "0.12.3",
		}
		block := &core.Block{
			Header:       header,
			Transactions: []core.Transaction{tx},
		}
		declaredClass := &core.DeclaredClass{
			At:    3002,
			Class: &core.Cairo1Class{},
		}

		mockReader.EXPECT().Receipt(hash).Return(nil, header.Hash, header.Number, nil)
		mockReader.EXPECT().BlockByNumber(header.Number).Return(block, nil)

		mockReader.EXPECT().StateAtBlockHash(header.ParentHash).Return(nil, nopCloser, nil)
		headState := mocks.NewMockStateHistoryReader(mockCtrl)
		headState.EXPECT().Class(tx.ClassHash).Return(declaredClass, nil)
		mockReader.EXPECT().HeadState().Return(headState, nopCloser, nil)

		vmTrace := json.RawMessage(`{
		"validate_invocation": {"contract_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "entry_point_selector": "0x162da33a4585851fe8d3af3c2a9c60b557814e221e0d4f30ff0b2189d9c7775", "calldata": ["0x2", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "0x0", "0x3", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "0x3", "0x10", "0x13", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0x0", "class_hash": "0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": [], "calls": [{"contract_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "entry_point_selector": "0x162da33a4585851fe8d3af3c2a9c60b557814e221e0d4f30ff0b2189d9c7775", "calldata": ["0x2", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "0x0", "0x3", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "0x3", "0x10", "0x13", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0x0", "class_hash": "0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": [], "calls": [], "events": [], "messages": []}], "events": [], "messages": []},
		"execute_invocation": {"contract_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "entry_point_selector": "0x15d40a3d6ca2ac30f4031e42be28da9b056fef9bb7357ac5e85627ee876e5ad", "calldata": ["0x2", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "0x0", "0x3", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "0x3", "0x10", "0x13", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0x0", "class_hash": "0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1", "0x1"], "calls": [{"contract_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "entry_point_selector": "0x15d40a3d6ca2ac30f4031e42be28da9b056fef9bb7357ac5e85627ee876e5ad", "calldata": ["0x2", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "0x0", "0x3", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "0x3", "0x10", "0x13", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0x0", "class_hash": "0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1", "0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x134692b230b9e1ffa39098904722134159652b09c5bc41d88d6698779d228ff"], "data": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"]}], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "entry_point_selector": "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "calldata": ["0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x5ee939756c1a60b029c594da00e637bf5923bf04a86ff163e877e899c0840eb", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "entry_point_selector": "0x1171593aa5bdadda4d6b0efde6cc94ee7649c3163d5efeb19da6c16d63a2a63", "calldata": ["0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x420eeb770f7a4", "0x0", "0x40139799e37e4", "0x0", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x0", "0x0", "0x1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x64"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x38627c278c0b3cb3c84ddee2c783fb22c3c3a3f0e667ea2b82be0ea2253bce4", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x41b033f4a31df8067c24d1e9b550a2ce75fd4a29e1147af9752174f0e6cb20", "calldata": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x41b033f4a31df8067c24d1e9b550a2ce75fd4a29e1147af9752174f0e6cb20", "calldata": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"], "data": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x1e8480", "0x0"]}], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x1ed6790cdca923073adc728080b06c159d9784cc9bf8fb26181acfdbe4256e6", "entry_point_selector": "0x260bb04cf90403013190e77d7e75f3d40d3d307180364da33c63ff53061d4e8", "calldata": [], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x5ee939756c1a60b029c594da00e637bf5923bf04a86ff163e877e899c0840eb", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x0", "0x0", "0x5"], "calls": [{"contract_address": "0x1ed6790cdca923073adc728080b06c159d9784cc9bf8fb26181acfdbe4256e6", "entry_point_selector": "0x260bb04cf90403013190e77d7e75f3d40d3d307180364da33c63ff53061d4e8", "calldata": [], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x46668cd07d83af5d7158e7cd62c710f1a7573501bcd4f4092c6a4e1ecd2bf61", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x0", "0x0", "0x5"], "calls": [], "events": [], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1e8480", "0x0"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1e8480", "0x0"], "calls": [], "events": [], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "entry_point_selector": "0x15543c3708653cda9d418b4ccd3be11368e40636c10c44b18cfe756b6d88b29", "calldata": ["0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x0", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x0", "0x0", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x2ceb6369dba6af865bca639f9f1342dfb1ae4e5d0d0723de98028b812e7cdd2", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": [], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "calldata": ["0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x1e8480", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x219209e083275171774dab1df80982e9df2096516f06319c5c6d71ae0a8480c", "calldata": ["0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x1e8480", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x134692b230b9e1ffa39098904722134159652b09c5bc41d88d6698779d228ff"], "data": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x1e8480", "0x0"]}], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "entry_point_selector": "0x2c0f7bf2d6cf5304c29171bf493feb222fef84bdaf17805a6574b0c2e8bcc87", "calldata": ["0x1e8480", "0x0", "0x0", "0x0", "0x2", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x648f780a"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x514718bb56ed2a8607554c7d393c2ffd73cbab971c120b00a2ce27cc58dd1c1", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x2", "0x1e8480", "0x0", "0x417c36e4fc16d", "0x0"], "calls": [{"contract_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "entry_point_selector": "0x3c388f7eb137a89061c6f0b6e78bae453202258b0b3c419f8dd9814a547d406", "calldata": [], "caller_address": "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "class_hash": "0x231adde42526bad434ca2eb983efdd64472638702f87f97e6e3c084f264e06f", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x178b60b3a0bcc4aa98", "0xaf07589b7c", "0x648f7422"], "calls": [], "events": [], "messages": []}, {"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x41b033f4a31df8067c24d1e9b550a2ce75fd4a29e1147af9752174f0e6cb20", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "0x1e8480", "0x0"], "caller_address": "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x41b033f4a31df8067c24d1e9b550a2ce75fd4a29e1147af9752174f0e6cb20", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "0x1e8480", "0x0"], "caller_address": "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"], "data": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "0x1e8480", "0x0"]}], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "entry_point_selector": "0x15543c3708653cda9d418b4ccd3be11368e40636c10c44b18cfe756b6d88b29", "calldata": ["0x417c36e4fc16d", "0x0", "0x0", "0x0", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "class_hash": "0x231adde42526bad434ca2eb983efdd64472638702f87f97e6e3c084f264e06f", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": [], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x417c36e4fc16d", "0x0"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x417c36e4fc16d", "0x0"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"], "data": ["0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0x417c36e4fc16d", "0x0"]}], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x178b5c9bdd4e74e92b", "0x0"], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x178b5c9bdd4e74e92b", "0x0"], "calls": [], "events": [], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0x52c7ba99c77fc38dd3346beea6c0753c3471f2e3135af5bb837d6c9523fff62", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0xaf07771ffc", "0x0"], "calls": [{"contract_address": "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325"], "caller_address": "0x23c72abdf49dffc85ae3ede714f2168ad384cc67d08524732acea90df325", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0xaf07771ffc", "0x0"], "calls": [], "events": [], "messages": []}], "events": [], "messages": []}], "events": [{"keys": ["0xe14a408baf7f453312eec68e9b7d728ec5337fbdf671f917ee8c80f3255232"], "data": ["0x178b5c9bdd4e74e92b", "0xaf07771ffc"]}, {"keys": ["0xe316f0d9d2a3affa97de1d99bb2aac0538e2666d0d8545545ead241ef0ccab"], "data": ["0x7a6f98c03379b9513ca84cca1373ff452a7462a3b61598f0af5bb27ad7f76d1", "0x0", "0x0", "0x1e8480", "0x0", "0x417c36e4fc16d", "0x0", "0x0", "0x0", "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"]}], "messages": []}], "events": [], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x417c36e4fc16d", "0x0"], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x2e4263afad30923c891518314c3c95dbe830a16874e8abc5777a9a20b54c76e", "calldata": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x417c36e4fc16d", "0x0"], "calls": [], "events": [], "messages": []}], "events": [], "messages": []}, {"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x417c36e4fc16d", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x417c36e4fc16d", "0x0"], "caller_address": "0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"], "data": ["0x4270219d365d6b017231b52e92b3fb5d7c8378b05e9abc97724537a80e93b0f", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x417c36e4fc16d", "0x0"]}], "messages": []}], "events": [], "messages": []}], "events": [{"keys": ["0xe316f0d9d2a3affa97de1d99bb2aac0538e2666d0d8545545ead241ef0ccab"], "data": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x53c91253bc9682c04929ca02ed00b3e423f6710d2ee7e0d5ebb06f3ecf368a8", "0x1e8480", "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "0x417c36e4fc16d", "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a"]}], "messages": []}], "events": [], "messages": []}], "events": [{"keys": ["0x5ad857f66a5b55f1301ff1ed7e098ac6d4433148f0b72ebc4a2945ab85ad53"], "data": ["0x2fc5e96de394697c1311606c96ec14840e408493fd42cf0c54b73b39d312b81", "0x2", "0x1", "0x1"]}], "messages": []}], "events": [], "messages": []},
		"fee_transfer_invocation": {"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8", "0x2cb6", "0x0"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3", "entry_point_type": "EXTERNAL", "call_type": "CALL", "result": ["0x1"], "calls": [{"contract_address": "0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7", "entry_point_selector": "0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e", "calldata": ["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8", "0x2cb6", "0x0"], "caller_address": "0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "class_hash": "0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0", "entry_point_type": "EXTERNAL", "call_type": "DELEGATE", "result": ["0x1"], "calls": [], "events": [{"keys": ["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"], "data": ["0xd747220b2744d8d8d48c8a52bd3869fb98aea915665ab2485d5eadb49def6a", "0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8", "0x2cb6", "0x0"]}], "messages": []}], "events": [], "messages": []}
	}`)
		mockVM.EXPECT().Execute([]core.Transaction{tx}, []core.Class{declaredClass.Class}, header.Number, header.Timestamp, header.SequencerAddress,
			gomock.Any(), utils.Mainnet, []*felt.Felt{}, false, false, false, gomock.Any(), gomock.Any(), false).Return(nil, []json.RawMessage{vmTrace}, nil)

		trace, err := handler.TraceTransaction(context.Background(), *hash)
		require.Nil(t, err)
		assert.Equal(t, vmTrace, trace)
	})
}

func TestSimulateTransactions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	const network = utils.Mainnet

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, network, nil, nil, mockVM, "", log)

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)
	mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil).AnyTimes()
	mockReader.EXPECT().HeadsHeader().Return(&core.Header{}, nil).AnyTimes()
	sequencerAddress := core.NetworkBlockHashMetaInfo(network).FallBackSequencerAddress

	t.Run("ok with zero values, skip fee", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, true, false, false, nil, nil, false).
			Return([]*felt.Felt{}, []json.RawMessage{}, nil)

		_, err := handler.SimulateTransactions(rpc.BlockID{Latest: true}, []rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipFeeChargeFlag})
		require.Nil(t, err)
	})

	t.Run("ok with zero values, skip validate", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, false, true, false, nil, nil, false).
			Return([]*felt.Felt{}, []json.RawMessage{}, nil)

		_, err := handler.SimulateTransactions(rpc.BlockID{Latest: true}, []rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipValidateFlag})
		require.Nil(t, err)
	})

	t.Run("transaction execution error", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, false, true, false, nil, nil, false).
			Return(nil, nil, vm.TransactionExecutionError{
				Index: 44,
				Cause: errors.New("oops"),
			})

		_, err := handler.SimulateTransactions(rpc.BlockID{Latest: true}, []rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipValidateFlag})
		require.Equal(t, rpc.ErrTransactionExecutionError.CloneWithData(rpc.TransactionExecutionErrorData{
			TransactionIndex: 44,
			ExecutionError:   "oops",
		}), err)

		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, false, true, true, nil, nil, true).
			Return(nil, nil, vm.TransactionExecutionError{
				Index: 44,
				Cause: errors.New("oops"),
			})

		_, err = handler.LegacySimulateTransactions(rpc.BlockID{Latest: true}, []rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipValidateFlag})
		require.Equal(t, rpc.ErrContractError.CloneWithData(rpc.ContractErrorData{
			RevertError: "oops",
		}), err)
	})
}

func TestTraceBlockTransactions(t *testing.T) {
	errTests := map[string]rpc.BlockID{
		"latest":  {Latest: true},
		"pending": {Pending: true},
		"hash":    {Hash: new(felt.Felt).SetUint64(1)},
		"number":  {Number: 1},
	}

	for description, id := range errTests {
		t.Run(description, func(t *testing.T) {
			log := utils.NewNopZapLogger()
			network := utils.Mainnet
			chain := blockchain.New(pebble.NewMemTest(t), network, log)
			handler := rpc.New(chain, nil, network, nil, nil, nil, "", log)

			update, rpcErr := handler.TraceBlockTransactions(context.Background(), id)
			assert.Nil(t, update)
			assert.Equal(t, rpc.ErrBlockNotFound, rpcErr)
		})
	}

	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()

	const network = utils.Mainnet
	handler := rpc.New(mockReader, nil, network, nil, nil, mockVM, "", log)

	t.Run("pending block", func(t *testing.T) {
		blockHash := utils.HexToFelt(t, "0x0001")
		header := &core.Header{
			// hash is not set because it's pending block
			ParentHash:      utils.HexToFelt(t, "0x0C3"),
			Number:          0,
			GasPrice:        utils.HexToFelt(t, "0x777"),
			ProtocolVersion: "0.12.3",
		}
		l1Tx := &core.L1HandlerTransaction{
			TransactionHash: utils.HexToFelt(t, "0x000000C"),
		}
		declaredClass := &core.DeclaredClass{
			At:    3002,
			Class: &core.Cairo1Class{},
		}
		declareTx := &core.DeclareTransaction{
			TransactionHash: utils.HexToFelt(t, "0x000000001"),
			ClassHash:       utils.HexToFelt(t, "0x00000BC00"),
		}
		block := &core.Block{
			Header:       header,
			Transactions: []core.Transaction{l1Tx, declareTx},
		}

		mockReader.EXPECT().BlockByHash(blockHash).Return(block, nil)
		state := mocks.NewMockStateHistoryReader(mockCtrl)
		mockReader.EXPECT().StateAtBlockHash(header.ParentHash).Return(state, nopCloser, nil)
		headState := mocks.NewMockStateHistoryReader(mockCtrl)
		headState.EXPECT().Class(declareTx.ClassHash).Return(declaredClass, nil)
		mockReader.EXPECT().PendingState().Return(headState, nopCloser, nil)
		const height uint64 = 8
		mockReader.EXPECT().Height().Return(height, nil)

		sequencerAddress := core.NetworkBlockHashMetaInfo(network).FallBackSequencerAddress
		paidL1Fees := []*felt.Felt{(&felt.Felt{}).SetUint64(1)}
		vmTrace := json.RawMessage(`{
			"validate_invocation": {},
			"execute_invocation": {},
			"fee_transfer_invocation": {}
		}`)
		mockVM.EXPECT().Execute(block.Transactions, []core.Class{declaredClass.Class}, height+1, header.Timestamp, sequencerAddress,
			gomock.Any(), network, paidL1Fees, false, false, false, header.GasPrice, header.GasPriceSTRK, false).Return(nil, []json.RawMessage{vmTrace, vmTrace}, nil)

		result, err := handler.TraceBlockTransactions(context.Background(), rpc.BlockID{Hash: blockHash})
		require.Nil(t, err)
		assert.Equal(t, vmTrace, result[0].TraceRoot)
		assert.Equal(t, l1Tx.TransactionHash, result[0].TransactionHash)
	})
	t.Run("regular block", func(t *testing.T) {
		blockHash := utils.HexToFelt(t, "0x37b244ea7dc6b3f9735fba02d183ef0d6807a572dd91a63cc1b14b923c1ac0")
		tx := &core.DeclareTransaction{
			TransactionHash: utils.HexToFelt(t, "0x000000001"),
			ClassHash:       utils.HexToFelt(t, "0x000000000"),
		}

		header := &core.Header{
			Hash:             blockHash,
			ParentHash:       utils.HexToFelt(t, "0x0"),
			Number:           0,
			SequencerAddress: utils.HexToFelt(t, "0X111"),
			GasPrice:         utils.HexToFelt(t, "0x777"),
			ProtocolVersion:  "0.12.3",
		}
		block := &core.Block{
			Header:       header,
			Transactions: []core.Transaction{tx},
		}
		declaredClass := &core.DeclaredClass{
			At:    3002,
			Class: &core.Cairo1Class{},
		}

		mockReader.EXPECT().BlockByHash(blockHash).Return(block, nil)

		mockReader.EXPECT().StateAtBlockHash(header.ParentHash).Return(nil, nopCloser, nil)
		headState := mocks.NewMockStateHistoryReader(mockCtrl)
		headState.EXPECT().Class(tx.ClassHash).Return(declaredClass, nil)
		mockReader.EXPECT().HeadState().Return(headState, nopCloser, nil)

		vmTrace := json.RawMessage(`{
			"validate_invocation":{"entry_point_selector":"0x36fcbf06cd96843058359e1a75928beacfac10727dab22a3972f0af8aa92895","calldata":["0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2","0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463","0x2","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x0"],"caller_address":"0x0","class_hash":"0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[{"entry_point_selector":"0x36fcbf06cd96843058359e1a75928beacfac10727dab22a3972f0af8aa92895","calldata":["0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2","0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463","0x2","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x0"],"caller_address":"0x0","class_hash":"0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2","entry_point_type":"EXTERNAL","call_type":"DELEGATE","result":[],"calls":[],"events":[],"messages":[]}],"events":[],"messages":[]},
			"execute_invocation":{"entry_point_selector":"0x28ffe4ff0f226a9107253e17a904099aa4f63a02a5621de0576e5aa71bc5194","calldata":["0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2","0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463","0x2","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x0"],"caller_address":"0x0","class_hash":"0x25ec026985a3bf9d0cc1fe17326b245dfdc3ff89b8fde106542a3ea56c5a918","entry_point_type":"CONSTRUCTOR","call_type":"CALL","result":[],"calls":[{"entry_point_selector":"0x79dc0da7c54b95f10aa182ad0a46400db63156920adb65eca2654c0945a463","calldata":["0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x0"],"caller_address":"0x0","class_hash":"0x33434ad846cdd5f23eb73ff09fe6fddd568284a0fb7d1be20ee482f044dabe2","entry_point_type":"EXTERNAL","call_type":"DELEGATE","result":[],"calls":[],"events":[{"keys":["0x10c19bef19acd19b2c9f4caa40fd47c9fbe1d9f91324d44dcd36be2dae96784"],"data":["0xdac9bcffb3d967f19a7fe21002c98c984d5a9458a88e6fc5d1c478a97ed412","0x322258135d04971e96b747a5551061aa046ad5d8be11a35c67029d96b23f98","0x0"]}],"messages":[]}],"events":[],"messages":[]},
			"fee_transfer_invocation":{"entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x5dcd266a80b8a5f29f04d779c6b166b80150c24f2180a75e82427242dab20a9","0x15be","0x0"],"caller_address":"0xdac9bcffb3d967f19a7fe21002c98c984d5a9458a88e6fc5d1c478a97ed412","class_hash":"0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3","entry_point_type":"EXTERNAL","call_type":"CALL","result":["0x1"],"calls":[{"entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x5dcd266a80b8a5f29f04d779c6b166b80150c24f2180a75e82427242dab20a9","0x15be","0x0"],"caller_address":"0xdac9bcffb3d967f19a7fe21002c98c984d5a9458a88e6fc5d1c478a97ed412","class_hash":"0x2760f25d5a4fb2bdde5f561fd0b44a3dee78c28903577d37d669939d97036a0","entry_point_type":"EXTERNAL","call_type":"DELEGATE","result":["0x1"],"calls":[],"events":[{"keys":["0x99cd8bde557814842a3121e8ddfd433a539b8c9f14bf31ebf108d12e6196e9"],"data":["0xdac9bcffb3d967f19a7fe21002c98c984d5a9458a88e6fc5d1c478a97ed412","0x5dcd266a80b8a5f29f04d779c6b166b80150c24f2180a75e82427242dab20a9","0x15be","0x0"]}],"messages":[]}],"events":[],"messages":[]}}
		}`)
		mockVM.EXPECT().Execute([]core.Transaction{tx}, []core.Class{declaredClass.Class}, header.Number, header.Timestamp, header.SequencerAddress,
			gomock.Any(), network, []*felt.Felt{}, false, false, false, header.GasPrice, header.GasPriceSTRK, false).Return(nil, []json.RawMessage{vmTrace}, nil)

		expectedResult := []rpc.TracedBlockTransaction{
			{
				TransactionHash: tx.Hash(),
				TraceRoot:       vmTrace,
			},
		}
		result, err := handler.TraceBlockTransactions(context.Background(), rpc.BlockID{Hash: blockHash})
		require.Nil(t, err)
		assert.Equal(t, expectedResult, result)
	})
}

func TestRpcBlockAdaptation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockReader := mocks.NewMockReader(mockCtrl)
	handler := rpc.New(mockReader, nil, utils.Goerli, nil, nil, nil, "", nil)

	client := feeder.NewTestClient(t, utils.Goerli)
	gw := adaptfeeder.New(client)
	latestBlockNumber := uint64(485004)

	t.Run("default sequencer address", func(t *testing.T) {
		latestBlock, err := gw.BlockByNumber(context.Background(), latestBlockNumber)
		require.NoError(t, err)
		latestBlock.Header.SequencerAddress = nil
		mockReader.EXPECT().Head().Return(latestBlock, nil).Times(2)
		mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).Times(2)

		block, rpcErr := handler.BlockWithTxs(rpc.BlockID{Latest: true})
		require.NoError(t, err, rpcErr)
		require.Equal(t, &felt.Zero, block.BlockHeader.SequencerAddress)

		blockWithTxHashes, rpcErr := handler.BlockWithTxHashes(rpc.BlockID{Latest: true})
		require.NoError(t, err, rpcErr)
		require.Equal(t, &felt.Zero, blockWithTxHashes.BlockHeader.SequencerAddress)
	})
}

type fakeConn struct {
	w io.Writer
}

func (fc *fakeConn) Write(p []byte) (int, error) {
	return fc.w.Write(p)
}

func (fc *fakeConn) Equal(other jsonrpc.Conn) bool {
	fc2, ok := other.(*fakeConn)
	if !ok {
		return false
	}
	return fc.w == fc2.w
}

func TestSubscribeNewHeadsAndUnsubscribe(t *testing.T) {
	log := utils.NewNopZapLogger()
	network := utils.Mainnet
	client := feeder.NewTestClient(t, network)
	gw := adaptfeeder.New(client)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	chain := blockchain.New(pebble.NewMemTest(t), network, log)
	syncer := sync.New(chain, gw, log, 0, false)
	handler := rpc.New(chain, syncer, network, nil, nil, nil, "", log)
	go func() {
		require.NoError(t, handler.Run(ctx))
	}()
	// Technically, there's a race between goroutine above and the SubscribeNewHeads call down below.
	// Sleep for a moment just in case.
	time.Sleep(50 * time.Millisecond)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		require.NoError(t, serverConn.Close())
		require.NoError(t, clientConn.Close())
	})

	// Subscribe without setting the connection on the context.
	id, rpcErr := handler.SubscribeNewHeads(ctx)
	require.Zero(t, id)
	require.Equal(t, jsonrpc.MethodNotFound, rpcErr.Code)

	// Sync blocks and then revert head.
	// This is a super hacky way to deterministically receive a single block on the subscription.
	// It would be nicer if we could tell the synchronizer to exit after a certain block height, but, alas, we can't do that.
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	require.NoError(t, syncer.Run(syncCtx))
	syncCancel()
	// This is technically an unsafe thing to do. We're modifying the synchronizer's blockchain while it is owned by the synchronizer.
	// But it works.
	require.NoError(t, chain.RevertHead())

	// Subscribe.
	subCtx := context.WithValue(ctx, jsonrpc.ConnKey{}, &fakeConn{w: serverConn})
	id, rpcErr = handler.SubscribeNewHeads(subCtx)
	require.Nil(t, rpcErr)

	// Sync the block we reverted above.
	syncCtx, syncCancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
	require.NoError(t, syncer.Run(syncCtx))
	syncCancel()

	// Receive a block header.
	want := `{"jsonrpc":"2.0","method":"juno_subscribeNewHeads","params":{"result":{"block_hash":"0x4e1f77f39545afe866ac151ac908bd1a347a2a8a7d58bef1276db4f06fdf2f6","parent_hash":"0x2a70fb03fe363a2d6be843343a1d81ce6abeda1e9bd5cc6ad8fa9f45e30fdeb","block_number":2,"new_root":"0x3ceee867d50b5926bb88c0ec7e0b9c20ae6b537e74aac44b8fcf6bb6da138d9","timestamp":1637084470,"sequencer_address":"0x0","l1_gas_price":{"price_in_fri":"0x0","price_in_wei":"0x0"},"starknet_version":""},"subscription":%d}}`
	want = fmt.Sprintf(want, id)
	got := make([]byte, len(want))
	_, err := clientConn.Read(got)
	require.NoError(t, err)
	require.Equal(t, want, string(got))

	// Unsubscribe without setting the connection on the context.
	ok, rpcErr := handler.Unsubscribe(ctx, id)
	require.Equal(t, jsonrpc.MethodNotFound, rpcErr.Code)
	require.False(t, ok)

	// Unsubscribe on correct connection with the incorrect id.
	ok, rpcErr = handler.Unsubscribe(subCtx, id+1)
	require.Equal(t, rpc.ErrSubscriptionNotFound, rpcErr)
	require.False(t, ok)

	// Unsubscribe on incorrect connection with the correct id.
	subCtx = context.WithValue(context.Background(), jsonrpc.ConnKey{}, &fakeConn{})
	ok, rpcErr = handler.Unsubscribe(subCtx, id)
	require.Equal(t, rpc.ErrSubscriptionNotFound, rpcErr)
	require.False(t, ok)

	// Unsubscribe on correct connection with the correct id.
	subCtx = context.WithValue(context.Background(), jsonrpc.ConnKey{}, &fakeConn{w: serverConn})
	ok, rpcErr = handler.Unsubscribe(subCtx, id)
	require.Nil(t, rpcErr)
	require.True(t, ok)
}

func TestMultipleSubscribeNewHeadsAndUnsubscribe(t *testing.T) {
	log := utils.NewNopZapLogger()
	network := utils.Mainnet
	feederClient := feeder.NewTestClient(t, network)
	gw := adaptfeeder.New(feederClient)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	chain := blockchain.New(pebble.NewMemTest(t), network, log)
	syncer := sync.New(chain, gw, log, 0, false)
	handler := rpc.New(chain, syncer, network, nil, nil, nil, "", log)
	go func() {
		require.NoError(t, handler.Run(ctx))
	}()
	// Technically, there's a race between goroutine above and the SubscribeNewHeads call down below.
	// Sleep for a moment just in case.
	time.Sleep(50 * time.Millisecond)

	// Sync blocks and then revert head.
	// This is a super hacky way to deterministically receive a single block on the subscription.
	// It would be nicer if we could tell the synchronizer to exit after a certain block height, but, alas, we can't do that.
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	require.NoError(t, syncer.Run(syncCtx))
	syncCancel()
	// This is technically an unsafe thing to do. We're modifying the synchronizer's blockchain while it is owned by the synchronizer.
	// But it works.
	require.NoError(t, chain.RevertHead())

	server := jsonrpc.NewServer(1, log)
	require.NoError(t, server.RegisterMethods(jsonrpc.Method{
		Name:    "juno_subscribeNewHeads",
		Handler: handler.SubscribeNewHeads,
	}, jsonrpc.Method{
		Name:    "juno_unsubscribe",
		Params:  []jsonrpc.Parameter{{Name: "id"}},
		Handler: handler.Unsubscribe,
	}))
	ws := jsonrpc.NewWebsocket(server, log)
	httpSrv := httptest.NewServer(ws)
	conn1, _, err := websocket.Dial(ctx, httpSrv.URL, nil)
	require.NoError(t, err)
	conn2, _, err := websocket.Dial(ctx, httpSrv.URL, nil)
	require.NoError(t, err)

	subscribeMsg := []byte(`{"jsonrpc":"2.0","id":1,"method":"juno_subscribeNewHeads"}`)

	firstID := uint64(1)
	secondID := uint64(2)
	handler.WithIDGen(func() uint64 { return firstID })
	require.NoError(t, conn1.Write(ctx, websocket.MessageText, subscribeMsg))

	want := `{"jsonrpc":"2.0","result":%d,"id":1}`
	firstWant := fmt.Sprintf(want, firstID)
	_, firstGot, err := conn1.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, firstWant, string(firstGot))

	handler.WithIDGen(func() uint64 { return secondID })
	require.NoError(t, conn2.Write(ctx, websocket.MessageText, subscribeMsg))
	secondWant := fmt.Sprintf(want, secondID)
	_, secondGot, err := conn2.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, secondWant, string(secondGot))

	// Now we're subscribed. Sync the block we reverted above.
	syncCtx, syncCancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
	require.NoError(t, syncer.Run(syncCtx))
	syncCancel()

	// Receive a block header.
	want = `{"jsonrpc":"2.0","method":"juno_subscribeNewHeads","params":{"result":{"block_hash":"0x4e1f77f39545afe866ac151ac908bd1a347a2a8a7d58bef1276db4f06fdf2f6","parent_hash":"0x2a70fb03fe363a2d6be843343a1d81ce6abeda1e9bd5cc6ad8fa9f45e30fdeb","block_number":2,"new_root":"0x3ceee867d50b5926bb88c0ec7e0b9c20ae6b537e74aac44b8fcf6bb6da138d9","timestamp":1637084470,"sequencer_address":"0x0","l1_gas_price":{"price_in_fri":"0x0","price_in_wei":"0x0"},"starknet_version":""},"subscription":%d}}`
	firstWant = fmt.Sprintf(want, firstID)
	_, firstGot, err = conn1.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, firstWant, string(firstGot))
	secondWant = fmt.Sprintf(want, secondID)
	_, secondGot, err = conn2.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, secondWant, string(secondGot))

	// Unsubscribe
	unsubMsg := `{"jsonrpc":"2.0","id":1,"method":"juno_unsubscribe","params":[%d]}`
	require.NoError(t, conn1.Write(ctx, websocket.MessageBinary, []byte(fmt.Sprintf(unsubMsg, firstID))))
	require.NoError(t, conn2.Write(ctx, websocket.MessageBinary, []byte(fmt.Sprintf(unsubMsg, secondID))))
}

func TestTraceFallback(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	network := utils.Integration
	client := feeder.NewTestClient(t, network)
	mockReader := mocks.NewMockReader(mockCtrl)
	gateway := adaptfeeder.New(client)
	handler := rpc.New(mockReader, nil, network, nil, client, nil, "", nil)

	mockReader.EXPECT().BlockByNumber(gomock.Any()).DoAndReturn(func(number uint64) (block *core.Block, err error) {
		return gateway.BlockByNumber(context.Background(), number)
	}).AnyTimes()
	mockReader.EXPECT().L1Head().Return(nil, db.ErrKeyNotFound).AnyTimes()

	tests := map[string]struct {
		hash        string
		blockNumber uint64
		want        string
	}{
		"old block": {
			hash:        "0x3ae41b0f023e53151b0c8ab8b9caafb7005d5f41c9ab260276d5bdc49726279",
			blockNumber: 0,
			want:        `[{"trace_root":{"type":"DEPLOY","constructor_invocation":{"contract_address":"0x7b196a359045d4d0c10f73bdf244a9e1205a615dbb754b8df40173364288534","calldata":["0x187d50a5cf3ebd6d4d6fa8e29e4cad0a237759c6416304a25c4ea792ed4bba4","0x42f5af30d6693674296ad87301935d0c159036c3b24af4042ff0270913bf6c6"],"caller_address":"0x0","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":29}}},"transaction_hash":"0x3fa1bff0c86f34b2eb32c26d12208b6bdb4a5f6a434ac1d4f0e2d1db71bd711"},{"trace_root":{"type":"DEPLOY","constructor_invocation":{"contract_address":"0x64ed79a8ebe97485d3357bbfdf5f6bea0d9db3b5f1feb6e80d564a179122dc6","calldata":["0x5cedec15acd969b0fba39fec9e7d9bd4d0b33f100969ad3a4543039a6f696d4","0xce9801d27b02543f4d88b60aa456860f94ee9f612fc56464abfbdeedc1ab72"],"caller_address":"0x0","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":29}}},"transaction_hash":"0x154c02cc3165cceadaa32e7238a67061b3a1eac414138c4ebe1408f37fd93eb"},{"trace_root":{"type":"INVOKE","execute_invocation":{"contract_address":"0x64ed79a8ebe97485d3357bbfdf5f6bea0d9db3b5f1feb6e80d564a179122dc6","calldata":["0x17d9c35a8b9a0d4512fa05eafec01c2758a7a5b7ec7b47408a24a4b33124d9b","0x2","0x7f800b5bf79637f8f83f47a8fc4d368b43695c781b22a899f11b5f2faba874a","0x3a7a40d383612b0ad167aec8d90fb07e576e017d07948f63ac318b52511ae93"],"caller_address":"0x0","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":165,"memory_holes":22,"pedersen_builtin_applications":2,"range_check_builtin_applications":7}}},"transaction_hash":"0x7893675c16da857b7c4229cda449e08a4fe13b07ca817e79d1db02e8a046047"},{"trace_root":{"type":"INVOKE","execute_invocation":{"contract_address":"0x64ed79a8ebe97485d3357bbfdf5f6bea0d9db3b5f1feb6e80d564a179122dc6","calldata":["0x17d9c35a8b9a0d4512fa05eafec01c2758a7a5b7ec7b47408a24a4b33124d9b","0x2","0x7f800b5bf79637f8f83f47a8fc4d368b43695c781b22a899f11b5f2faba874a","0xf140b304e9266c72f1054116dd06d9c1c8e981db7bf34e3c6da99640e9a7c8"],"caller_address":"0x0","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":165,"memory_holes":22,"pedersen_builtin_applications":2,"range_check_builtin_applications":7}}},"transaction_hash":"0x4a277d67e3f42c4a343854081d1e2e9e425f1323255e4486d2badb37a1d8630"}]`,
		},
		"newer block": {
			hash:        "0xe3828bd9154ab385e2cbb95b3b650365fb3c6a4321660d98ce8b0a9194f9a3",
			blockNumber: 300000,
			want:        `[{"trace_root":{"type":"INVOKE","validate_invocation":{"contract_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","entry_point_selector":"0x162da33a4585851fe8d3af3c2a9c60b557814e221e0d4f30ff0b2189d9c7775","calldata":["0x1","0x332299dc083f3778122e5b7762bc9d399da18fefe93769aee67bb49f51c8d2","0x2d7cf5d5a324a320f9f37804b1615a533fde487400b41af80f13f7ac5581325","0x0","0x4","0x4","0xaf35ee8ed700ff132c5d1d298a73becda25ccdf9","0x2","0x6cd852fe1b2bbd8587bb0aaeb09813436c57c8ce21e75651e317273a1f22228","0x58feb991988e53fffcba71f6df23c803fb062f1b3bab126d2c9ce574255b36e"],"caller_address":"0x0","class_hash":"0x646a72e2aab2fca75d713fbe4a58f2d12cbd64105621b89dc9ce7045b5bf02b","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":89,"range_check_builtin_applications":2,"ecdsa_builtin_applications":1}},"execute_invocation":{"contract_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","entry_point_selector":"0x15d40a3d6ca2ac30f4031e42be28da9b056fef9bb7357ac5e85627ee876e5ad","calldata":["0x1","0x332299dc083f3778122e5b7762bc9d399da18fefe93769aee67bb49f51c8d2","0x2d7cf5d5a324a320f9f37804b1615a533fde487400b41af80f13f7ac5581325","0x0","0x4","0x4","0xaf35ee8ed700ff132c5d1d298a73becda25ccdf9","0x2","0x6cd852fe1b2bbd8587bb0aaeb09813436c57c8ce21e75651e317273a1f22228","0x58feb991988e53fffcba71f6df23c803fb062f1b3bab126d2c9ce574255b36e"],"caller_address":"0x0","class_hash":"0x646a72e2aab2fca75d713fbe4a58f2d12cbd64105621b89dc9ce7045b5bf02b","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[{"contract_address":"0x332299dc083f3778122e5b7762bc9d399da18fefe93769aee67bb49f51c8d2","entry_point_selector":"0x2d7cf5d5a324a320f9f37804b1615a533fde487400b41af80f13f7ac5581325","calldata":["0xaf35ee8ed700ff132c5d1d298a73becda25ccdf9","0x2","0x6cd852fe1b2bbd8587bb0aaeb09813436c57c8ce21e75651e317273a1f22228","0x58feb991988e53fffcba71f6df23c803fb062f1b3bab126d2c9ce574255b36e"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0x165e7db96ab97a63c621229617a6d49633737238673477a54720e4c952f2c7e","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[],"events":[],"messages":[{"order":0,"to_address":"0xaf35ee8ed700ff132c5d1d298a73becda25ccdf9","payload":["0x6cd852fe1b2bbd8587bb0aaeb09813436c57c8ce21e75651e317273a1f22228","0x58feb991988e53fffcba71f6df23c803fb062f1b3bab126d2c9ce574255b36e"]}],"execution_resources":{"steps":233,"memory_holes":1,"range_check_builtin_applications":5}}],"events":[],"messages":[],"execution_resources":{"steps":374,"memory_holes":4,"range_check_builtin_applications":7}},"fee_transfer_invocation":{"contract_address":"0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7","entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8","0x127089df3a1984","0x0"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3","entry_point_type":"EXTERNAL","call_type":"CALL","result":["0x1"],"calls":[{"contract_address":"0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7","entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8","0x127089df3a1984","0x0"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0x28d7d394810ad8c52741ad8f7564717fd02c10ced68657a81d0b6710ce22079","entry_point_type":"EXTERNAL","call_type":"DELEGATE","result":["0x1"],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":488,"memory_holes":40,"pedersen_builtin_applications":4,"range_check_builtin_applications":21}}],"events":[],"messages":[],"execution_resources":{"steps":548,"memory_holes":40,"pedersen_builtin_applications":4,"range_check_builtin_applications":21}}},"transaction_hash":"0x2a648ab1aa6847eb38507fc842e050f256562bf87b26083c332f3f21318c2c3"},{"trace_root":{"type":"INVOKE","validate_invocation":{"contract_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","entry_point_selector":"0x162da33a4585851fe8d3af3c2a9c60b557814e221e0d4f30ff0b2189d9c7775","calldata":["0x1","0x5f9211b05c9609d54a8bf5f9cfa4e2cd5a3cab3b5d79682c585575495a15dd1","0x317eb442b72a9fae758d4fb26830ed0d9f31c8e7da4dbff4e8c59ea6a158e7f","0x0","0x4","0x4","0x447379c077035ef4f442411d0407ce9aa66c558f0060137f6455f4f230eabeb","0x2","0x6811b7755a7dd0ec1fb6f51a883e3f255368e2dfd497b5f6480c00cf9cd5a2e","0x23b9e26720dd7aaf98c7cea56499f48f75dc1d4123f7e2d6c23bfc4d5f4a336"],"caller_address":"0x0","class_hash":"0x646a72e2aab2fca75d713fbe4a58f2d12cbd64105621b89dc9ce7045b5bf02b","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":89,"range_check_builtin_applications":2,"ecdsa_builtin_applications":1}},"execute_invocation":{"contract_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","entry_point_selector":"0x15d40a3d6ca2ac30f4031e42be28da9b056fef9bb7357ac5e85627ee876e5ad","calldata":["0x1","0x5f9211b05c9609d54a8bf5f9cfa4e2cd5a3cab3b5d79682c585575495a15dd1","0x317eb442b72a9fae758d4fb26830ed0d9f31c8e7da4dbff4e8c59ea6a158e7f","0x0","0x4","0x4","0x447379c077035ef4f442411d0407ce9aa66c558f0060137f6455f4f230eabeb","0x2","0x6811b7755a7dd0ec1fb6f51a883e3f255368e2dfd497b5f6480c00cf9cd5a2e","0x23b9e26720dd7aaf98c7cea56499f48f75dc1d4123f7e2d6c23bfc4d5f4a336"],"caller_address":"0x0","class_hash":"0x646a72e2aab2fca75d713fbe4a58f2d12cbd64105621b89dc9ce7045b5bf02b","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[{"contract_address":"0x5f9211b05c9609d54a8bf5f9cfa4e2cd5a3cab3b5d79682c585575495a15dd1","entry_point_selector":"0x317eb442b72a9fae758d4fb26830ed0d9f31c8e7da4dbff4e8c59ea6a158e7f","calldata":["0x447379c077035ef4f442411d0407ce9aa66c558f0060137f6455f4f230eabeb","0x2","0x6811b7755a7dd0ec1fb6f51a883e3f255368e2dfd497b5f6480c00cf9cd5a2e","0x23b9e26720dd7aaf98c7cea56499f48f75dc1d4123f7e2d6c23bfc4d5f4a336"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0x13abfd2f333f9c69f690f1569140cdae25f6f66e3f371c9cbb998b65f664a85","entry_point_type":"EXTERNAL","call_type":"CALL","result":[],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":166,"memory_holes":22,"pedersen_builtin_applications":2,"range_check_builtin_applications":7}}],"events":[],"messages":[],"execution_resources":{"steps":307,"memory_holes":25,"pedersen_builtin_applications":2,"range_check_builtin_applications":9}},"fee_transfer_invocation":{"contract_address":"0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7","entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8","0x3b2d25cd7bccc","0x0"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0xd0e183745e9dae3e4e78a8ffedcce0903fc4900beace4e0abf192d4c202da3","entry_point_type":"EXTERNAL","call_type":"CALL","result":["0x1"],"calls":[{"contract_address":"0x49d36570d4e46f48e99674bd3fcc84644ddd6b96f7c741b1562b82f9e004dc7","entry_point_selector":"0x83afd3f4caedc6eebf44246fe54e38c95e3179a5ec9ea81740eca5b482d12e","calldata":["0x1176a1bd84444c89232ec27754698e5d2e7e1a7f1539f12027f28b23ec9f3d8","0x3b2d25cd7bccc","0x0"],"caller_address":"0x58b7ee817bd2978c7657d05d3131e83e301ed1aa79d5ad16f01925fd52d1da7","class_hash":"0x28d7d394810ad8c52741ad8f7564717fd02c10ced68657a81d0b6710ce22079","entry_point_type":"EXTERNAL","call_type":"DELEGATE","result":["0x1"],"calls":[],"events":[],"messages":[],"execution_resources":{"steps":488,"memory_holes":40,"pedersen_builtin_applications":4,"range_check_builtin_applications":21}}],"events":[],"messages":[],"execution_resources":{"steps":548,"memory_holes":40,"pedersen_builtin_applications":4,"range_check_builtin_applications":21}}},"transaction_hash":"0xbc984e8e1fe594dd518a3a51db4f338437a5d2fbdda772d4426b532a67ffff"}]`,
		},
	}

	for description, test := range tests {
		t.Run(description, func(t *testing.T) {
			mockReader.EXPECT().BlockByHash(utils.HexToFelt(t, test.hash)).DoAndReturn(func(_ *felt.Felt) (block *core.Block, err error) {
				return mockReader.BlockByNumber(test.blockNumber)
			})
			trace, jErr := handler.TraceBlockTransactions(context.Background(), rpc.BlockID{Number: test.blockNumber})
			require.Nil(t, jErr)
			jsonStr, err := json.Marshal(trace)
			require.NoError(t, err)
			assert.JSONEq(t, test.want, string(jsonStr))
		})
	}
}

func TestThrottledVMError(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	mockReader := mocks.NewMockReader(mockCtrl)
	network := utils.Integration
	mockVM := mocks.NewMockVM(mockCtrl)

	throttledVM := node.NewThrottledVM(mockVM, 0, 0)
	handler := rpc.New(mockReader, nil, network, nil, nil, throttledVM, "", nil)
	mockState := mocks.NewMockStateHistoryReader(mockCtrl)

	t.Run("call", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockReader.EXPECT().HeadsHeader().Return(new(core.Header), nil)
		mockState.EXPECT().ContractClassHash(&felt.Zero).Return(new(felt.Felt), nil)
		_, rpcErr := handler.Call(rpc.FunctionCall{}, rpc.BlockID{Latest: true})
		assert.Equal(t, utils.ErrResourceBusy.Error(), rpcErr.Data)
	})

	t.Run("simulate", func(t *testing.T) {
		mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil)
		mockReader.EXPECT().HeadsHeader().Return(&core.Header{}, nil)
		_, rpcErr := handler.SimulateTransactions(rpc.BlockID{Latest: true}, []rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipFeeChargeFlag})
		assert.Equal(t, utils.ErrResourceBusy.Error(), rpcErr.Data)
	})

	t.Run("trace", func(t *testing.T) {
		blockHash := utils.HexToFelt(t, "0x0001")
		header := &core.Header{
			// hash is not set because it's pending block
			ParentHash:      utils.HexToFelt(t, "0x0C3"),
			Number:          0,
			GasPrice:        utils.HexToFelt(t, "0x777"),
			ProtocolVersion: "0.12.3",
		}
		l1Tx := &core.L1HandlerTransaction{
			TransactionHash: utils.HexToFelt(t, "0x000000C"),
		}
		declaredClass := &core.DeclaredClass{
			At:    3002,
			Class: &core.Cairo1Class{},
		}
		declareTx := &core.DeclareTransaction{
			TransactionHash: utils.HexToFelt(t, "0x000000001"),
			ClassHash:       utils.HexToFelt(t, "0x00000BC00"),
		}
		block := &core.Block{
			Header:       header,
			Transactions: []core.Transaction{l1Tx, declareTx},
		}

		mockReader.EXPECT().BlockByHash(blockHash).Return(block, nil)
		state := mocks.NewMockStateHistoryReader(mockCtrl)
		mockReader.EXPECT().StateAtBlockHash(header.ParentHash).Return(state, nopCloser, nil)
		headState := mocks.NewMockStateHistoryReader(mockCtrl)
		headState.EXPECT().Class(declareTx.ClassHash).Return(declaredClass, nil)
		mockReader.EXPECT().PendingState().Return(headState, nopCloser, nil)
		const height uint64 = 8
		mockReader.EXPECT().Height().Return(height, nil)
		_, rpcErr := handler.TraceBlockTransactions(context.Background(), rpc.BlockID{Hash: blockHash})
		assert.Equal(t, utils.ErrResourceBusy.Error(), rpcErr.Data)
	})
}

func TestSpecVersion(t *testing.T) {
	handler := rpc.New(nil, nil, 0, nil, nil, nil, "", nil)
	version, rpcErr := handler.SpecVersion()
	require.Nil(t, rpcErr)
	require.Equal(t, "0.6.0", version)

	legacyVersion, rpcErr := handler.LegacySpecVersion()
	require.Nil(t, rpcErr)
	require.Equal(t, "0.5.1", legacyVersion)
}

func TestEstimateFee(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	const network = utils.Mainnet

	mockReader := mocks.NewMockReader(mockCtrl)
	mockVM := mocks.NewMockVM(mockCtrl)
	log := utils.NewNopZapLogger()
	handler := rpc.New(mockReader, nil, network, nil, nil, mockVM, "", log)

	mockState := mocks.NewMockStateHistoryReader(mockCtrl)
	mockReader.EXPECT().HeadState().Return(mockState, nopCloser, nil).AnyTimes()
	mockReader.EXPECT().HeadsHeader().Return(&core.Header{}, nil).AnyTimes()
	sequencerAddress := core.NetworkBlockHashMetaInfo(network).FallBackSequencerAddress

	t.Run("ok with zero values", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, true, false, true, nil, nil, false).
			Return([]*felt.Felt{}, []json.RawMessage{}, nil)

		_, err := handler.EstimateFee([]rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{}, rpc.BlockID{Latest: true})
		require.Nil(t, err)
	})

	t.Run("ok with zero values, skip validate", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, true, true, true, nil, nil, false).
			Return([]*felt.Felt{}, []json.RawMessage{}, nil)

		_, err := handler.EstimateFee([]rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipValidateFlag}, rpc.BlockID{Latest: true})
		require.Nil(t, err)
	})

	t.Run("transaction execution error", func(t *testing.T) {
		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, true, true, true, nil, nil, false).
			Return(nil, nil, vm.TransactionExecutionError{
				Index: 44,
				Cause: errors.New("oops"),
			})

		_, err := handler.EstimateFee([]rpc.BroadcastedTransaction{}, []rpc.SimulationFlag{rpc.SkipValidateFlag}, rpc.BlockID{Latest: true})
		require.Equal(t, rpc.ErrTransactionExecutionError.CloneWithData(rpc.TransactionExecutionErrorData{
			TransactionIndex: 44,
			ExecutionError:   "oops",
		}), err)

		mockVM.EXPECT().Execute(nil, nil, uint64(0), uint64(0), sequencerAddress, mockState, network, []*felt.Felt{}, true, false, true, nil, nil, true).
			Return(nil, nil, vm.TransactionExecutionError{
				Index: 44,
				Cause: errors.New("oops"),
			})

		_, err = handler.LegacyEstimateFee([]rpc.BroadcastedTransaction{}, rpc.BlockID{Latest: true})
		require.Equal(t, rpc.ErrContractError.CloneWithData(rpc.ContractErrorData{
			RevertError: "oops",
		}), err)
	})
}
