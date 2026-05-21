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
	"context"
	"errors"
	"iter"
	"slices"
	"strings"

	"github.com/docker/oci"
)

func (u unifier) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	r0, r1 := both(u, func(r oci.Interface, _ int) iter.Seq2[string, error] {
		return r.Repositories(ctx, startAfter)
	})
	return mergeIter(r0, r1, strings.Compare)
}

func (u unifier) Tags(ctx context.Context, repo string, params *oci.TagsParameters) iter.Seq2[string, error] {
	r0, r1 := both(u, func(r oci.Interface, _ int) iter.Seq2[string, error] {
		return r.Tags(ctx, repo, params)
	})
	it := mergeIter(r0, r1, strings.Compare)
	var limit int
	if params != nil {
		limit = params.Limit
	}
	if limit > 0 {
		return oci.LimitIter(it, limit)
	}
	return it
}

func (u unifier) Referrers(ctx context.Context, repo string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	r0, r1 := both(u, func(r oci.Interface, _ int) iter.Seq2[oci.Descriptor, error] {
		return r.Referrers(ctx, repo, digest, params)
	})
	return mergeIter(r0, r1, compareDescriptor)
}

func compareDescriptor(d0, d1 oci.Descriptor) int {
	return strings.Compare(d0.Digest.String(), d1.Digest.String())
}

func mergeIter[T any](it0, it1 iter.Seq2[T, error], cmp func(T, T) int) iter.Seq2[T, error] {
	// TODO streaming merge sort
	xs0, err0 := oci.All(it0)
	xs1, err1 := oci.All(it1)
	if err0 != nil || err1 != nil {
		notFound0 := errors.Is(err0, oci.ErrNameUnknown)
		notFound1 := errors.Is(err1, oci.ErrNameUnknown)
		if notFound0 && notFound1 {
			return oci.ErrorSeq[T](err0)
		}
		if notFound0 {
			err0 = nil
		}
		if notFound1 {
			err1 = nil
		}
	}
	var xs []T
	if len(xs0)+len(xs1) > 0 {
		xs = slices.Concat(xs0, xs1)
		slices.SortFunc(xs, cmp)
		xs = slices.CompactFunc(xs, func(t1, t2 T) bool {
			return cmp(t1, t2) == 0
		})
	}
	err := err0
	if err == nil {
		err = err1
	}
	if err == nil {
		return oci.SliceSeq(xs)
	}
	return func(yield func(T, error) bool) {
		for _, x := range xs {
			if !yield(x, nil) {
				return
			}
		}
		yield(*new(T), err)
	}
}
