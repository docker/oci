// Copyright 2023 CUE Labs AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ociunify

import (
	"cmp"
	"iter"
	"testing"

	"github.com/docker/oci"
	"github.com/stretchr/testify/require"
)

var mergeIterTests = []struct {
	testName string
	it0, it1 iter.Seq2[int, error]
	want     []int
	wantErr  error
}{{
	testName: "IdenticalContents",
	it0:      oci.SliceSeq([]int{1, 2, 3}),
	it1:      oci.SliceSeq([]int{1, 2, 3}),
	want:     []int{1, 2, 3},
}, {
	testName: "DifferentContents",
	it0:      oci.SliceSeq([]int{0, 1, 2, 3}),
	it1:      oci.SliceSeq([]int{1, 2, 3, 5}),
	want:     []int{0, 1, 2, 3, 5},
}, {
	testName: "NoItems",
	it0:      oci.SliceSeq[int](nil),
	it1:      oci.SliceSeq[int](nil),
	want:     []int{},
}}

func TestMergeIter(t *testing.T) {
	for _, test := range mergeIterTests {
		t.Run(test.testName, func(t *testing.T) {
			it := mergeIter(test.it0, test.it1, cmp.Compare)
			xs, err := oci.All(it)
			require.Equal(t, test.want, xs)
			require.Equal(t, test.wantErr, err)
		})
	}
}
