// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lcs

// This file defines the abstract sequence over which the LCS algorithm operates.

// sequences abstracts a pair of sequences, A and B.
type sequences interface {
	lengths() (int, int)                    // len(A), len(B)
	commonPrefixLen(ai, aj, bi, bj int) int // len(commonPrefix(A[ai:aj], B[bi:bj]))
	commonSuffixLen(ai, aj, bi, bj int) int // len(commonSuffix(A[ai:aj], B[bi:bj]))
}

type linesSeqs struct{ a, b []string }

func (s linesSeqs) lengths() (int, int) { return len(s.a), len(s.b) }
func (s linesSeqs) commonPrefixLen(ai, aj, bi, bj int) int {
	return commonPrefixLen(s.a[ai:aj], s.b[bi:bj])
}
func (s linesSeqs) commonSuffixLen(ai, aj, bi, bj int) int {
	return commonSuffixLen(s.a[ai:aj], s.b[bi:bj])
}

// commonPrefixLen returns the length of the common prefix of a[ai:aj] and b[bi:bj].
func commonPrefixLen[T comparable](a, b []T) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffixLen returns the length of the common suffix of a[ai:aj] and b[bi:bj].
func commonSuffixLen[T comparable](a, b []T) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}
