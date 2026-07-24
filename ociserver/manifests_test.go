package ociserver

import "testing"

func TestAcceptsMediaType(t *testing.T) {
	t.Parallel()

	const manifestMediaType = "application/vnd.oci.image.manifest.v1+json"

	tests := []struct {
		name         string
		acceptHeader []string
		want         bool
	}{
		{
			name: "empty accept allows stored type",
			want: true,
		},
		{
			name:         "exact match",
			acceptHeader: []string{manifestMediaType},
			want:         true,
		},
		{
			name:         "exact match with parameters",
			acceptHeader: []string{manifestMediaType + "; q=0.8"},
			want:         true,
		},
		{
			name:         "multiple values with match",
			acceptHeader: []string{"application/vnd.oci.image.index.v1+json, " + manifestMediaType},
			want:         true,
		},
		{
			name: "multiple header lines with match",
			acceptHeader: []string{
				"application/vnd.oci.image.index.v1+json",
				manifestMediaType,
			},
			want: true,
		},
		{
			name:         "any type wildcard",
			acceptHeader: []string{"*/*"},
			want:         true,
		},
		{
			name:         "subtype wildcard",
			acceptHeader: []string{"application/*"},
			want:         true,
		},
		{
			name:         "quality zero rejects otherwise matching type",
			acceptHeader: []string{manifestMediaType + "; q=0"},
			want:         false,
		},
		{
			name:         "different type",
			acceptHeader: []string{"application/vnd.oci.image.index.v1+json"},
			want:         false,
		},
		{
			name:         "different top level wildcard",
			acceptHeader: []string{"text/*"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := acceptsMediaType(tt.acceptHeader, manifestMediaType); got != tt.want {
				t.Fatalf("acceptsMediaType(%q, %q) = %v, want %v", tt.acceptHeader, manifestMediaType, got, tt.want)
			}
		})
	}
}
