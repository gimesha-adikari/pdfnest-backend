package studio

import (
	"errors"
	"testing"
)

type sourceMetadataReaderStub struct {
	metadata map[string]string
	err      error
}

func (r sourceMetadataReaderStub) GetMetadataPDF(string, string) (map[string]string, error) {
	return r.metadata, r.err
}

func TestCanonicalSourceMetadataNormalizesVisibleFieldsOnly(t *testing.T) {
	got := canonicalSourceMetadata(map[string]string{
		" title ": "  Source title  ", "AUTHOR": "Author", "subject": "Subject",
		"keywords": " a, b ", "Creator": "not exposed",
	})
	want := map[string]string{"Title": "Source title", "Author": "Author", "Subject": "Subject", "Keywords": "a, b"}
	if len(got) != len(want) {
		t.Fatalf("got keys %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if _, ok := got["Creator"]; ok {
		t.Fatal("unexposed Creator metadata must not enter the VDM")
	}
}

func TestHydrateSourceMetadataUsesReaderAndEmptyFallback(t *testing.T) {
	reader := sourceMetadataReaderStub{metadata: map[string]string{"title": "Hydrated"}}
	got := hydrateSourceMetadata(reader, "/tmp/source.pdf")
	if got["Title"] != "Hydrated" || got["Author"] != "" {
		t.Fatalf("unexpected hydrated metadata: %#v", got)
	}

	got = hydrateSourceMetadata(sourceMetadataReaderStub{err: errors.New("worker unavailable")}, "/tmp/source.pdf")
	for _, key := range []string{"Title", "Author", "Subject", "Keywords"} {
		if got[key] != "" {
			t.Errorf("fallback %s = %q, want empty", key, got[key])
		}
	}
}
