package engineapi

import (
	"context"
	"fmt"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/execution/rlp"
	"github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/rpc"
)

// InsertRawBlocksV1 stages blocks without validating execution or changing forkchoice.
func (t *testingImpl) InsertRawBlocksV1(ctx context.Context, rawBlocks, rawBALs []hexutil.Bytes) ([]common.Hash, error) {
	if len(rawBlocks) == 0 || len(rawBlocks) > 16 || len(rawBlocks) != len(rawBALs) {
		return nil, &rpc.InvalidParamsError{Message: "require 1 to 16 blocks and one BAL per block"}
	}
	parent := t.server.chainRW.CurrentHeader(ctx)
	if parent == nil {
		return nil, fmt.Errorf("current canonical header unavailable")
	}
	blocks := make([]*types.Block, len(rawBlocks))
	hashes := make([]common.Hash, len(rawBlocks))
	for i, raw := range rawBlocks {
		var block types.Block
		if err := rlp.DecodeBytes(raw, &block); err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}
		header := block.HeaderNoCopy()
		if !header.Number.IsUint64() || header.Number.Uint64() != parent.Number.Uint64()+1 || header.ParentHash != parent.Hash() {
			return nil, &rpc.InvalidParamsError{Message: fmt.Sprintf("block %d must extend the current head or prior batch block", i)}
		}
		bal, err := types.DecodeBlockAccessListSidecar(rawBALs[i])
		if err != nil {
			return nil, fmt.Errorf("block %d BAL: %w", i, err)
		}
		if err := bal.ValidateForBlock(header.GasLimit); err != nil {
			return nil, fmt.Errorf("block %d BAL: %w", i, err)
		}
		balHash, err := bal.Hash()
		if err != nil {
			return nil, err
		}
		if header.BlockAccessListHash == nil || *header.BlockAccessListHash != balHash {
			return nil, &rpc.InvalidParamsError{Message: fmt.Sprintf("block %d BAL hash mismatch", i)}
		}
		blocks[i] = block.WithBlockAccessListSidecar(bal)
		hashes[i] = block.Hash()
		parent = header
	}
	if err := t.server.chainRW.InsertBlocks(ctx, blocks); err != nil {
		return nil, err
	}
	return hashes, nil
}
