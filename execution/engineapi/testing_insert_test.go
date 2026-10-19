package engineapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/execution/execmodule"
	"github.com/erigontech/erigon/execution/rlp"
	"github.com/erigontech/erigon/execution/types"
)

type rawBatchInserter interface {
	InsertRawBlocksV1(context.Context, []hexutil.Bytes, []hexutil.Bytes) ([]common.Hash, error)
}

type rawBatchContinuation interface {
	InsertRawBlocksV2(context.Context, common.Hash, []hexutil.Bytes, []hexutil.Bytes) ([]common.Hash, error)
}

func TestInsertRawBlocksV2ExtendsStagedDescendant(t *testing.T) {
	canonical := makeParentHeader(1000)
	raw, bals, hashes := rawBatchFixture(t, canonical, 128)
	headers := map[common.Hash]*types.Header{canonical.Hash(): canonical}
	for _, data := range raw[:112] {
		var block types.Block
		require.NoError(t, rlp.DecodeBytes(data, &block))
		headers[block.Hash()] = block.Header()
	}
	inserted := false
	stub := &stubExecutionModule{
		currentHeaderFunc: func(context.Context) (*types.Header, error) { return canonical, nil },
		getHeaderFunc: func(_ context.Context, hash *common.Hash, _ *uint64) (*types.Header, error) {
			return headers[*hash], nil
		},
		insertBlocksFunc: func(_ context.Context, blocks []*types.Block) (execmodule.ExecutionStatus, error) {
			require.Len(t, blocks, 16)
			for i, block := range blocks {
				require.Equal(t, hashes[112+i], block.Hash())
			}
			inserted = true
			return execmodule.ExecutionStatusSuccess, nil
		},
		validateChainFunc: func(context.Context, common.Hash, uint64) (execmodule.ValidationResult, error) {
			t.Fatal("staging must not execute candidate forks")
			return execmodule.ValidationResult{}, nil
		},
		updateForkChoiceFunc: func(context.Context, common.Hash, common.Hash, common.Hash) (execmodule.ForkChoiceResult, error) {
			t.Fatal("staging must not advance canonical head")
			return execmodule.ForkChoiceResult{}, nil
		},
	}
	api, ok := newTestingAPI(allForksChainConfig(), stub).(rawBatchContinuation)
	require.True(t, ok, "testing API must support bounded staged descendants")
	got, err := api.InsertRawBlocksV2(t.Context(), hashes[111], raw[112:], bals[112:])
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, hashes[112:], got)
}

func TestInsertRawBlocksV2RejectsUnboundedOrUnrelatedParent(t *testing.T) {
	for _, kind := range []string{"too_far", "unrelated", "missing_ancestor", "missing_parent", "missing_canonical"} {
		t.Run(kind, func(t *testing.T) {
			canonical := makeParentHeader(1000)
			count := 3
			if kind == "too_far" {
				count = 129
			}
			raw, bals, hashes := rawBatchFixture(t, canonical, count)
			headers := map[common.Hash]*types.Header{canonical.Hash(): canonical}
			for _, data := range raw[:count-1] {
				var block types.Block
				require.NoError(t, rlp.DecodeBytes(data, &block))
				headers[block.Hash()] = block.Header()
			}
			switch kind {
			case "unrelated":
				canonical = types.CopyHeader(canonical)
				canonical.Extra = []byte("other chain")
			case "missing_ancestor":
				delete(headers, hashes[0])
			case "missing_parent":
				delete(headers, hashes[count-2])
			case "missing_canonical":
				canonical = nil
			}
			calls := 0
			stub := &stubExecutionModule{
				currentHeaderFunc: func(context.Context) (*types.Header, error) { return canonical, nil },
				getHeaderFunc: func(_ context.Context, hash *common.Hash, _ *uint64) (*types.Header, error) {
					return headers[*hash], nil
				},
				insertBlocksFunc: func(context.Context, []*types.Block) (execmodule.ExecutionStatus, error) {
					calls++
					return execmodule.ExecutionStatusSuccess, nil
				},
			}
			api := newTestingAPI(allForksChainConfig(), stub).(rawBatchContinuation)
			_, err := api.InsertRawBlocksV2(t.Context(), hashes[count-2], raw[count-1:], bals[count-1:])
			require.Error(t, err)
			require.Zero(t, calls)
		})
	}
}

func rawBatchFixture(t *testing.T, parent *types.Header, count int) ([]hexutil.Bytes, []hexutil.Bytes, []common.Hash) {
	t.Helper()
	raw := make([]hexutil.Bytes, count)
	bals := make([]hexutil.Bytes, count)
	hashes := make([]common.Hash, count)
	for i := range raw {
		header := makeParentHeader(parent.Time + 12)
		header.Number.SetUint64(parent.Number.Uint64() + 1)
		header.ParentHash = parent.Hash()
		zeroHash := common.Hash{}
		zero := uint64(0)
		header.WithdrawalsHash = &zeroHash
		header.BlobGasUsed = &zero
		header.ExcessBlobGas = &zero
		header.ParentBeaconBlockRoot = &zeroHash
		header.RequestsHash = &zeroHash
		header.SlotNumber = &zero
		sidecar := types.NewBlockAccessListSidecar(types.BlockAccessList{})
		balHash, err := sidecar.Hash()
		require.NoError(t, err)
		header.BlockAccessListHash = &balHash
		block := types.NewBlockFromNetwork(header, &types.Body{Withdrawals: []*types.Withdrawal{}}, sidecar)
		raw[i], err = rlp.EncodeToBytes(block)
		require.NoError(t, err)
		bals[i], err = sidecar.Bytes()
		require.NoError(t, err)
		hashes[i] = block.Hash()
		parent = header
	}
	return raw, bals, hashes
}

func TestInsertRawBlocksV1StagesBatchWithoutClaimingValidation(t *testing.T) {
	parent := makeParentHeader(1000)
	raw, bals, hashes := rawBatchFixture(t, parent, 2)
	inserted := false
	stub := &stubExecutionModule{
		currentHeaderFunc: func(context.Context) (*types.Header, error) { return parent, nil },
		insertBlocksFunc: func(_ context.Context, blocks []*types.Block) (execmodule.ExecutionStatus, error) {
			require.Len(t, blocks, 2)
			for i, block := range blocks {
				require.Equal(t, hashes[i], block.Hash())
				bytes, err := block.BlockAccessListSidecar().Bytes()
				require.NoError(t, err)
				require.Equal(t, []byte(bals[i]), bytes)
			}
			inserted = true
			return execmodule.ExecutionStatusSuccess, nil
		},
		validateChainFunc: func(context.Context, common.Hash, uint64) (execmodule.ValidationResult, error) {
			t.Fatal("staging must not execute individual candidate forks")
			return execmodule.ValidationResult{}, nil
		},
		updateForkChoiceFunc: func(context.Context, common.Hash, common.Hash, common.Hash) (execmodule.ForkChoiceResult, error) {
			t.Fatal("staging must not advance canonical head")
			return execmodule.ForkChoiceResult{}, nil
		},
	}
	api, ok := newTestingAPI(allForksChainConfig(), stub).(rawBatchInserter)
	require.True(t, ok, "testing API must expose bounded raw batch insertion")
	got, err := api.InsertRawBlocksV1(t.Context(), raw, bals)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, hashes, got)
}

func TestInsertRawBlocksV1RejectsInvalidBatchBeforeInsertion(t *testing.T) {
	for _, kind := range []string{"too_many", "empty", "wrong_parent", "wrong_bal_hash", "missing_bal_hash", "malformed_last", "missing_bal"} {
		t.Run(kind, func(t *testing.T) {
			parent := makeParentHeader(1000)
			count := 2
			if kind == "too_many" {
				count = 17
			}
			raw, bals, _ := rawBatchFixture(t, parent, count)
			switch kind {
			case "empty":
				raw, bals = nil, nil
			case "missing_bal":
				bals = bals[:1]
			case "malformed_last":
				raw[1] = hexutil.Bytes{0xff}
			case "wrong_parent", "wrong_bal_hash", "missing_bal_hash":
				var block types.Block
				require.NoError(t, rlp.DecodeBytes(raw[1], &block))
				header := block.Header()
				switch kind {
				case "wrong_parent":
					header.ParentHash = common.Hash{1}
				case "wrong_bal_hash":
					badHash := common.Hash{1}
					header.BlockAccessListHash = &badHash
				case "missing_bal_hash":
					header.BlockAccessListHash = nil
					header.SlotNumber = nil
				}
				var err error
				raw[1], err = rlp.EncodeToBytes(types.NewBlockFromNetwork(header, block.Body(), nil))
				require.NoError(t, err)
			}
			calls := 0
			stub := &stubExecutionModule{
				currentHeaderFunc: func(context.Context) (*types.Header, error) { return parent, nil },
				insertBlocksFunc: func(context.Context, []*types.Block) (execmodule.ExecutionStatus, error) {
					calls++
					return execmodule.ExecutionStatusSuccess, nil
				},
			}
			api := newTestingAPI(allForksChainConfig(), stub).(rawBatchInserter)
			_, err := api.InsertRawBlocksV1(t.Context(), raw, bals)
			require.Error(t, err)
			require.Zero(t, calls, "reject the entire input before inserting its valid prefix")
		})
	}
}
