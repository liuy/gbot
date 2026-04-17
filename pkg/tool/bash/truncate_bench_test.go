package bash

import (
	"strings"
	"testing"
)

func BenchmarkTruncateOutput_UnderLimit(b *testing.B) {
	s := strings.Repeat("line of output\n", 100) // ~1500 bytes, well under 30000
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		truncateOutput(s, 30000)
	}
}

func BenchmarkTruncateOutput_AtLimit(b *testing.B) {
	s := strings.Repeat("x", 30000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		truncateOutput(s, 30000)
	}
}

func BenchmarkTruncateOutput_OverLimit(b *testing.B) {
	s := strings.Repeat("line of output\n", 5000) // ~75KB, well over 30000
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		truncateOutput(s, 30000)
	}
}
