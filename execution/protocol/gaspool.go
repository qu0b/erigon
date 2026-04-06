// Copyright 2015 The go-ethereum Authors
// (original work)
// Copyright 2024 The Erigon Authors
// (modifications)
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package protocol

import (
	"fmt"
	"math"
	"sync"
)

// GasPool tracks the amount of gas available during execution of the transactions
// in a block. The zero value is a pool with zero gas available.
//
// EIP-8037 introduces two-dimensional block gas: regular and state.
// After each transaction, remaining = initial - max(cumulativeRegular, cumulativeState).
// This ensures the block builder stops including transactions once either
// dimension would exceed the block gas limit.
type GasPool struct {
	mu      sync.RWMutex
	gas     uint64 // remaining gas available
	blobGas uint64

	// EIP-8037: 2D gas tracking for Amsterdam.
	initial           uint64 // block gas limit (set via InitAmsterdam)
	cumulativeRegular uint64 // Σ per-tx regular gas
	cumulativeState   uint64 // Σ per-tx state gas
	amsterdam         bool   // true if 2D tracking is active
}

func NewGasPool(gas, blobGas uint64) *GasPool {
	return &GasPool{gas: gas, blobGas: blobGas}
}

func (gp *GasPool) Reset(amount, blobGas uint64) {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		gp.gas = amount
		gp.blobGas = blobGas
		gp.initial = 0
		gp.cumulativeRegular = 0
		gp.cumulativeState = 0
		gp.amsterdam = false
	}
}

// InitAmsterdam enables 2D gas tracking for EIP-8037.
// gasLimit is the block gas limit. priorRegular and priorState are the
// cumulative gas values from prior AddTransactions batches (0 for the first batch).
func (gp *GasPool) InitAmsterdam(gasLimit, priorRegular, priorState uint64) {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		gp.initial = gasLimit
		gp.cumulativeRegular = priorRegular
		gp.cumulativeState = priorState
		gp.amsterdam = true
	}
}

// AddGas makes gas available for execution.
func (gp *GasPool) AddGas(amount uint64) *GasPool {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		if gp.gas > math.MaxUint64-amount {
			panic("gas pool pushed above uint64")
		}
		gp.gas += amount
	}
	return gp
}

// SubGas deducts the given amount from the pool if enough gas is
// available and returns an error otherwise.
func (gp *GasPool) SubGas(amount uint64) error {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		if gp.gas < amount {
			return ErrGasLimitReached
		}
		gp.gas -= amount
	}
	return nil
}

// ReturnGasAmsterdam updates the pool after a transaction using 2D gas accounting.
// remaining = initial - max(cumulativeRegular, cumulativeState).
// This must be called instead of AddGas for Amsterdam blocks.
func (gp *GasPool) ReturnGasAmsterdam(txRegular, txState uint64) error {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		gp.cumulativeRegular += txRegular
		gp.cumulativeState += txState

		blockUsed := max(gp.cumulativeRegular, gp.cumulativeState)
		if gp.initial < blockUsed {
			return fmt.Errorf("%w: block gas overflow: initial %d, used %d (regular: %d, state: %d)",
				ErrGasLimitReached, gp.initial, blockUsed, gp.cumulativeRegular, gp.cumulativeState)
		}
		gp.gas = gp.initial - blockUsed
	}
	return nil
}

// Gas returns the amount of gas remaining in the pool.
func (gp *GasPool) Gas() uint64 {
	if gp == nil {
		return 0
	}
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	return gp.gas
}

// IsAmsterdam returns whether 2D gas tracking is active.
func (gp *GasPool) IsAmsterdam() bool {
	if gp == nil {
		return false
	}
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	return gp.amsterdam
}

// AmsterdamDimensions returns the per-dimension cumulative gas values.
func (gp *GasPool) AmsterdamDimensions() (regular, state uint64) {
	if gp == nil {
		return 0, 0
	}
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	return gp.cumulativeRegular, gp.cumulativeState
}

// AddBlobGas makes blob gas available for execution.
func (gp *GasPool) AddBlobGas(amount uint64) *GasPool {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		if gp.blobGas > math.MaxUint64-amount {
			panic("blob gas pool pushed above uint64")
		}
		gp.blobGas += amount
	}
	return gp
}

// SubBlobGas deducts the given amount from the pool if enough blob gas is available and returns an
// error otherwise.
func (gp *GasPool) SubBlobGas(amount uint64) error {
	if gp != nil {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		if gp.blobGas < amount {
			return ErrBlobGasLimitReached
		}
		gp.blobGas -= amount
	}
	return nil
}

// BlobGas returns the amount of blob gas remaining in the pool.
func (gp *GasPool) BlobGas() uint64 {
	if gp == nil {
		return 0
	}
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	return gp.blobGas
}

func (gp *GasPool) String() string {
	if gp == nil {
		return "gas: %0, blob_gas: 0"
	}
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	if gp.amsterdam {
		return fmt.Sprintf("gas: %d, blob_gas: %d (2D: regular=%d, state=%d, initial=%d)",
			gp.gas, gp.blobGas, gp.cumulativeRegular, gp.cumulativeState, gp.initial)
	}
	return fmt.Sprintf("gas: %d, blob_gas: %d", gp.gas, gp.blobGas)
}
