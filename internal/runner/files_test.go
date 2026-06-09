package runner

import (
	"testing"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

func TestDecodeFileContent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      wire.FileInput
		want    []byte
		wantErr bool
	}{
		{
			name: "absent encoding is utf8 verbatim",
			in:   wire.FileInput{Name: "a.txt", Content: wire.Ptr("hello")},
			want: []byte("hello"),
		},
		{
			name: "explicit utf8",
			in:   wire.FileInput{Name: "a.txt", Content: wire.Ptr("héllo"), Encoding: wire.FileInputEncodingUtf8},
			want: []byte("héllo"),
		},
		{
			name: "base64 round-trip",
			in:   wire.FileInput{Name: "blob.bin", Content: wire.Ptr("aGVsbG8="), Encoding: wire.FileInputEncodingBase64},
			want: []byte("hello"),
		},
		{
			name: "base64 arbitrary bytes",
			in:   wire.FileInput{Name: "blob.bin", Content: wire.Ptr("AAEC/w=="), Encoding: wire.FileInputEncodingBase64},
			want: []byte{0x00, 0x01, 0x02, 0xff},
		},
		{
			name:    "bad base64",
			in:      wire.FileInput{Name: "blob.bin", Content: wire.Ptr("not!!base64"), Encoding: wire.FileInputEncodingBase64},
			wantErr: true,
		},
		{
			name:    "unknown encoding",
			in:      wire.FileInput{Name: "a.txt", Content: wire.Ptr("x"), Encoding: "rot13"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeFileContent(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (bytes=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != string(tc.want) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeWorkspacePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "flat file", in: "a.txt", want: "a.txt"},
		{name: "subdir preserved", in: "data/x.csv", want: "data/x.csv"},
		{name: "deep subdir preserved", in: "a/b/c/d.bin", want: "a/b/c/d.bin"},
		{name: "leading ./ stripped", in: "./x", want: "x"},
		{name: "interior ./ cleaned", in: "data/./x.csv", want: "data/x.csv"},
		{name: "interior dotdot rejected", in: "data/../x.csv", wantErr: true},
		{name: "empty rejected", in: "", wantErr: true},
		{name: "dot rejected", in: ".", wantErr: true},
		{name: "absolute rejected", in: "/etc/passwd", wantErr: true},
		{name: "traversal rejected", in: "../escape", wantErr: true},
		{name: "deep traversal rejected", in: "a/../../escape", wantErr: true},
		{name: "trailing traversal rejected", in: "..", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := SanitizeWorkspacePath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("sanitize(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}
