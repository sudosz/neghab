package humanize

import (
	"testing"
)

func TestBytes(t *testing.T) {
	tests := []struct {
		name string
		n    uint64
		want string
	}{
		{name: "zero bytes", n: 0, want: "0 B"},
		{name: "single byte", n: 1, want: "1 B"},
		{name: "bytes", n: 512, want: "512 B"},
		{name: "exactly 1 KB", n: 1024, want: "1.00 KB"},
		{name: "kilobytes", n: 2048, want: "2.00 KB"},
		{name: "fractional KB", n: 1536, want: "1.50 KB"},
		{name: "exactly 1 MB", n: 1 << 20, want: "1.00 MB"},
		{name: "megabytes", n: 50 << 20, want: "50.00 MB"},
		{name: "fractional MB", n: (1 << 20) + (512 << 10), want: "1.50 MB"},
		{name: "exactly 1 GB", n: 1 << 30, want: "1.00 GB"},
		{name: "gigabytes", n: 10 << 30, want: "10.00 GB"},
		{name: "fractional GB", n: (2 << 30) + (512 << 20), want: "2.50 GB"},
		{name: "just below 1 KB", n: 1023, want: "1023 B"},
		{name: "just below 1 MB", n: (1 << 20) - 1, want: "1024.00 KB"},
		{name: "just below 1 GB", n: (1 << 30) - 1, want: "1024.00 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Bytes(tt.n)
			if got != tt.want {
				t.Errorf("Bytes(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
