package state

import (
	"fmt"
	"math/bits"
	"time"

	"github.com/spaolacci/murmur3"

	"github.com/erigontech/erigon/common/dbg"
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/seg"
)

var domainStorageUnionEnabled = dbg.EnvBool("DOMAIN_STORAGE_UNION", false)

type domainUnion struct {
	words []uint64
	first int
}

func (dt *DomainRoTx) lookupFiles(hash uint64) (visibleFiles, error) {
	if domainStorageUnionEnabled && dt.name == kv.StorageDomain {
		if _, err := dt.visible.prepareUnion(dt.d.Compression, *dt.salt, 512, 8<<30); err != nil {
			return nil, err
		}
	}
	u := dt.visible.union
	if u != nil && dt.visible.unionSalt == *dt.salt && !u.contains(hash) {
		return dt.files[:u.first], nil
	}
	return dt.files, nil
}

func (v *domainVisible) prepareUnion(compression seg.FileCompression, salt uint32, minFiles int, maxBytes int64) (*domainUnion, error) {
	v.unionOnce.Do(func() {
		started := time.Now()
		v.unionSalt = salt
		v.union, v.unionErr = buildDomainUnion(v.files, compression, salt, minFiles, maxBytes)
		if v.union != nil {
			log.Info("[domain-union] built", "domain", v.name, "files", len(v.files)-v.union.first,
				"bytes", len(v.union.words)*8, "elapsed", time.Since(started))
		}
	})
	if v.unionSalt != salt {
		return nil, nil
	}
	return v.union, v.unionErr
}

func (u *domainUnion) add(hash uint64) {
	mask := uint64(len(u.words)*64 - 1)
	delta := bits.RotateLeft64(hash, 32) | 1
	for range 4 {
		index := hash & mask
		u.words[index>>6] |= uint64(1) << (index & 63)
		hash += delta
	}
}

func (u *domainUnion) contains(hash uint64) bool {
	mask := uint64(len(u.words)*64 - 1)
	delta := bits.RotateLeft64(hash, 32) | 1
	for range 4 {
		index := hash & mask
		if u.words[index>>6]&(uint64(1)<<(index&63)) == 0 {
			return false
		}
		hash += delta
	}
	return true
}

func buildDomainUnion(files visibleFiles, compression seg.FileCompression, salt uint32, minFiles int, maxBytes int64) (*domainUnion, error) {
	first := len(files)
	var size, keys uint64
	for first > 0 {
		d := files[first-1].src.decompressor
		if d.Size() > maxBytes-int64(size) {
			break
		}
		size += uint64(d.Size())
		keys += uint64(d.Count() / 2)
		first--
	}
	if len(files)-first < minFiles {
		return nil, nil
	}
	bitCount := uint64(64)
	for bitCount < keys*12 && bitCount < 1<<30 {
		bitCount <<= 1
	}
	u := &domainUnion{words: make([]uint64, bitCount/64), first: first}
	var key []byte
	for _, file := range files[first:] {
		r := seg.NewReader(file.src.decompressor.MakeGetter(), compression)
		for r.HasNext() {
			key, _ = r.Next(key[:0])
			if !r.HasNext() {
				return nil, fmt.Errorf("incomplete key/value pair in %s", file.Fullpath())
			}
			hi, _ := murmur3.Sum128WithSeed(key, salt)
			u.add(hi)
			r.Skip()
		}
	}
	return u, nil
}
