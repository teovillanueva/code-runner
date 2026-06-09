package runner

import (
	"archive/tar"
	"io"
	"testing"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// readTar collects regular-file entries (name -> bytes) and the set of
// directory entries from a tar buffer produced by buildFilesTar.
func readTar(t *testing.T, b io.Reader) (map[string][]byte, map[string]bool) {
	t.Helper()
	files := map[string][]byte{}
	dirs := map[string]bool{}
	tr := tar.NewReader(b)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			dirs[hdr.Name] = true
		case tar.TypeReg:
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read %q: %v", hdr.Name, err)
			}
			files[hdr.Name] = data
		}
	}
	return files, dirs
}

func TestBuildFilesTar_Base64AndSubdirs(t *testing.T) {
	// {main.py utf8} + {data/in.csv utf8} + {blob.bin base64} → all three at
	// the right paths with correct bytes, with parent dir entries for data/.
	in := []wire.FileInput{
		{Name: "main.py", Content: wire.Ptr("print('hi')")},
		{Name: "data/in.csv", Content: wire.Ptr("a,b\n1,2\n"), Encoding: wire.FileInputEncodingUtf8},
		{Name: "blob.bin", Content: wire.Ptr("AAEC/w=="), Encoding: wire.FileInputEncodingBase64},
		{Name: "nested/deep/x.bin", Content: wire.Ptr("aGk="), Encoding: wire.FileInputEncodingBase64},
	}
	buf, err := buildFilesTar(in)
	if err != nil {
		t.Fatalf("buildFilesTar: %v", err)
	}
	files, dirs := readTar(t, buf)

	want := map[string]string{
		"main.py":           "print('hi')",
		"data/in.csv":       "a,b\n1,2\n",
		"nested/deep/x.bin": "hi",
	}
	for name, content := range want {
		got, ok := files[name]
		if !ok {
			t.Fatalf("missing tar entry %q (entries: %v)", name, keys(files))
		}
		if string(got) != content {
			t.Fatalf("entry %q = %q want %q", name, got, content)
		}
	}
	if got := files["blob.bin"]; string(got) != string([]byte{0x00, 0x01, 0x02, 0xff}) {
		t.Fatalf("blob.bin bytes = % x want 00 01 02 ff", got)
	}
	// Explicit parent dir entries (deterministic layout).
	for _, d := range []string{"data/", "nested/", "nested/deep/"} {
		if !dirs[d] {
			t.Fatalf("missing dir entry %q (dirs: %v)", d, keys2(dirs))
		}
	}
}

func TestBuildFilesTar_RejectsTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "/etc/passwd", "a/../../escape"} {
		_, err := buildFilesTar([]wire.FileInput{{Name: name, Content: wire.Ptr("x")}})
		if err == nil {
			t.Fatalf("expected error for %q, got nil", name)
		}
	}
}

func TestBuildFilesTar_RejectsBadBase64(t *testing.T) {
	_, err := buildFilesTar([]wire.FileInput{
		{Name: "blob.bin", Content: wire.Ptr("not!!base64"), Encoding: wire.FileInputEncodingBase64},
	})
	if err == nil {
		t.Fatal("expected error for bad base64, got nil")
	}
}

// TestBuildFilesTar_BackwardCompat proves a flat, no-encoding request produces
// exactly a single regular-file entry with verbatim bytes and no dir entries —
// identical to the pre-Phase-15 behavior.
func TestBuildFilesTar_BackwardCompat(t *testing.T) {
	buf, err := buildFilesTar([]wire.FileInput{{Name: "main.py", Content: wire.Ptr("print(1)")}})
	if err != nil {
		t.Fatalf("buildFilesTar: %v", err)
	}
	files, dirs := readTar(t, buf)
	if len(files) != 1 || string(files["main.py"]) != "print(1)" {
		t.Fatalf("files = %v want single main.py", keys(files))
	}
	if len(dirs) != 0 {
		t.Fatalf("expected no dir entries for a flat file, got %v", keys2(dirs))
	}
}

func TestStripWorkspacePrefix(t *testing.T) {
	cases := map[string]string{
		"workspace":           "",
		"workspace/":          "",
		"workspace/plot.png":  "plot.png",
		"workspace/out/x.png": "out/x.png",
		"./workspace/main.py": "main.py",
		"workspace/a/b/c.bin": "a/b/c.bin",
	}
	for in, want := range cases {
		if got := stripWorkspacePrefix(in); got != want {
			t.Fatalf("stripWorkspacePrefix(%q) = %q want %q", in, got, want)
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keys2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
