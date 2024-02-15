// This file contains a bunch of utility functions for bloom components.
// TODO: Find a better location for this package

package bloomutils

import (
	"math"
	"sort"

	"github.com/grafana/dskit/ring"
	"github.com/prometheus/common/model"
	"golang.org/x/exp/slices"

	v1 "github.com/grafana/loki/pkg/storage/bloom/v1"
)

type InstanceWithTokenRange struct {
	Instance           ring.InstanceDesc
	MinToken, MaxToken uint32
}

func (i InstanceWithTokenRange) Cmp(token uint32) v1.BoundsCheck {
	if token < i.MinToken {
		return v1.Before
	} else if token > i.MaxToken {
		return v1.After
	}
	return v1.Overlap
}

type InstancesWithTokenRange []InstanceWithTokenRange

func (i InstancesWithTokenRange) Contains(token uint32) bool {
	for _, instance := range i {
		if instance.Cmp(token) == v1.Overlap {
			return true
		}
	}
	return false
}

// GetInstanceWithTokenRange calculates the token range for a specific instance
// with given id based on the first token in the ring.
// This assumes that each instance in the ring is configured with only a single
// token.
func GetInstanceWithTokenRange(id string, instances []ring.InstanceDesc) (v1.FingerprintBounds, error) {

	// Sort instances -- they may not be sorted
	// because they're usually accessed by looking up the tokens (which are sorted)
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Tokens[0] < instances[j].Tokens[0]
	})

	idx := slices.IndexFunc(instances, func(inst ring.InstanceDesc) bool {
		return inst.Id == id
	})

	// instance with Id == id not found
	if idx == -1 {
		return v1.FingerprintBounds{}, ring.ErrInstanceNotFound
	}

	i := uint64(idx)
	n := uint64(len(instances))
	step := math.MaxUint64 / n

	minToken := model.Fingerprint(step * i)
	maxToken := model.Fingerprint(step*i + step - 1)
	if i == n-1 {
		// extend the last token tange to MaxUint32
		maxToken = math.MaxUint64
	}

	return v1.NewBounds(minToken, maxToken), nil
}

// GetInstancesWithTokenRanges calculates the token ranges for a specific
// instance with given id based on all tokens in the ring.
// If the instances in the ring are configured with a single token, such as the
// bloom compactor, use GetInstanceWithTokenRange() instead.
func GetInstancesWithTokenRanges(id string, instances []ring.InstanceDesc) InstancesWithTokenRange {
	servers := make([]InstanceWithTokenRange, 0, len(instances))
	it := NewInstanceSortMergeIterator(instances)
	var firstInst ring.InstanceDesc
	var lastToken uint32
	for it.Next() {
		if firstInst.Id == "" {
			firstInst = it.At().Instance
		}
		if it.At().Instance.Id == id {
			servers = append(servers, it.At())
		}
		lastToken = it.At().MaxToken
	}
	// append token range from lastToken+1 to MaxUint32
	// only if the instance with the first token is the current one
	if len(servers) > 0 && firstInst.Id == id {
		servers = append(servers, InstanceWithTokenRange{
			MinToken: lastToken + 1,
			MaxToken: math.MaxUint32,
			Instance: servers[0].Instance,
		})
	}
	return servers
}

// NewInstanceSortMergeIterator creates an iterator that yields instanceWithToken elements
// where the token of the elements are sorted in ascending order.
func NewInstanceSortMergeIterator(instances []ring.InstanceDesc) v1.Iterator[InstanceWithTokenRange] {

	tokenIters := make([]v1.PeekingIterator[v1.IndexedValue[uint32]], 0, len(instances))
	for i, inst := range instances {
		sort.Slice(inst.Tokens, func(a, b int) bool { return inst.Tokens[a] < inst.Tokens[b] })
		itr := v1.NewIterWithIndex(v1.NewSliceIter[uint32](inst.Tokens), i)
		tokenIters = append(tokenIters, v1.NewPeekingIter[v1.IndexedValue[uint32]](itr))
	}

	heapIter := v1.NewHeapIterator[v1.IndexedValue[uint32]](
		func(iv1, iv2 v1.IndexedValue[uint32]) bool {
			return iv1.Value() < iv2.Value()
		},
		tokenIters...,
	)

	prevToken := -1
	return v1.NewDedupingIter[v1.IndexedValue[uint32], InstanceWithTokenRange](
		func(iv v1.IndexedValue[uint32], iwtr InstanceWithTokenRange) bool {
			return false
		},
		func(iv v1.IndexedValue[uint32]) InstanceWithTokenRange {
			minToken, maxToken := uint32(prevToken+1), iv.Value()
			prevToken = int(maxToken)
			return InstanceWithTokenRange{
				Instance: instances[iv.Index()],
				MinToken: minToken,
				MaxToken: maxToken,
			}
		},
		func(iv v1.IndexedValue[uint32], iwtr InstanceWithTokenRange) InstanceWithTokenRange {
			panic("must not be called, because Eq() is always false")
		},
		v1.NewPeekingIter(heapIter),
	)
}
