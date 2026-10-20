package state

import (
	"sync"
	"testing"

	"github.com/spaolacci/murmur3"
	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon/common/background"
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/kv"
)

func domainUnionFixture(t *testing.T) *DomainRoTx {
	t.Helper()
	db, d := testDbAndDomain(t, log.New())
	tx, err := db.BeginRw(t.Context())
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	dt := d.beginForTests()
	w := dt.NewWriter()
	require.NoError(t, w.PutWithPrev([]byte("old-only"), []byte("old"), 1, nil))
	require.NoError(t, w.PutWithPrev([]byte("deleted"), []byte("value"), 2, nil))
	require.NoError(t, w.PutWithPrev([]byte("recent"), []byte("value"), 17, nil))
	require.NoError(t, w.DeleteWithPrev([]byte("deleted"), 33, []byte("value")))
	require.NoError(t, w.Flush(t.Context(), tx))
	w.Close()
	dt.Close()
	for step := range kv.Step(3) {
		require.NoError(t, d.collateBuildIntegrate(t.Context(), step, tx, background.NewProgressSet()))
	}
	dt = d.beginForTests()
	t.Cleanup(dt.Close)
	require.Len(t, dt.files, 3)
	return dt
}

func TestDomainUnionSharedOnlyWithinVisibleGeneration(t *testing.T) {
	dt := domainUnionFixture(t)
	const workers = 8
	filters := make([]*domainUnion, workers)
	errors := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			filters[i], errors[i] = dt.visible.prepareUnion(dt.d.Compression, *dt.salt, 2, 1<<20)
		})
	}
	wg.Wait()
	require.NotNil(t, filters[0])
	for i := range workers {
		require.NoError(t, errors[i])
		require.Same(t, filters[0], filters[i])
	}
	other := newDomainVisible(dt.name, dt.files)
	filter, err := other.prepareUnion(dt.d.Compression, *dt.salt, 2, 1<<20)
	require.NoError(t, err)
	require.NotNil(t, filter)
	require.NotSame(t, filters[0], filter)
	wrongSalt, err := dt.visible.prepareUnion(dt.d.Compression, *dt.salt+1, 2, 1<<20)
	require.NoError(t, err)
	require.Nil(t, wrongSalt)
}

func TestDomainUnionIncludesDeletedKeys(t *testing.T) {
	dt := domainUnionFixture(t)
	limit := dt.files[1].src.decompressor.Size() + dt.files[2].src.decompressor.Size()
	filter, err := buildDomainUnion(dt.files, dt.d.Compression, *dt.salt, 2, limit)
	require.NoError(t, err)
	require.NotNil(t, filter)
	require.Equal(t, 1, filter.first)
	for _, key := range []string{"recent", "deleted"} {
		hi, _ := murmur3.Sum128WithSeed([]byte(key), *dt.salt)
		require.True(t, filter.contains(hi), key)
	}
	hi, _ := murmur3.Sum128WithSeed([]byte("absent"), *dt.salt)
	require.False(t, filter.contains(hi))
}

func TestDomainUnionLookupPreservesOlderValuesAndBounds(t *testing.T) {
	dt := domainUnionFixture(t)
	limit := dt.files[1].src.decompressor.Size() + dt.files[2].src.decompressor.Size()
	filter, err := buildDomainUnion(dt.files, dt.d.Compression, *dt.salt, 2, limit)
	require.NoError(t, err)
	dt.visible.union = filter
	dt.visible.unionSalt = *dt.salt
	hi, _ := murmur3.Sum128WithSeed([]byte("old-only"), *dt.salt)
	files, err := dt.lookupFiles(hi)
	require.NoError(t, err)
	require.Len(t, files, 1)
	for _, tc := range []struct {
		key     string
		bound   uint64
		bounded bool
		value   string
		found   bool
		start   uint64
	}{
		{"old-only", 0, false, "old", true, 0},
		{"recent", 0, false, "value", true, 16},
		{"deleted", 0, false, "", true, 32},
		{"deleted", 15, true, "value", true, 0},
		{"recent", 15, true, "", false, 0},
		{"absent", 0, false, "", false, 0},
	} {
		value, found, start, _, err := dt.lookupLatestFromFiles([]byte(tc.key), make([]byte, 0), tc.bound, tc.bounded)
		require.NoError(t, err)
		require.Equal(t, tc.found, found, tc.key)
		require.Equal(t, tc.value, string(value), tc.key)
		require.Equal(t, tc.start, start, tc.key)
	}
}
