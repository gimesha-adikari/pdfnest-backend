package studio

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func intPtr(value int) *int { return &value }

func TestOrderedMergeInputsPlacesCurrentDocumentExactlyOnce(t *testing.T) {
	paths := []string{"B.pdf", "CURRENT.pdf", "A.pdf"}
	if !reflect.DeepEqual(paths, orderedMergeInputs([]string{"B.pdf", "A.pdf"}, "CURRENT.pdf", 1)) {
		t.Fatalf("unexpected order: %#v", orderedMergeInputs([]string{"B.pdf", "A.pdf"}, "CURRENT.pdf", 1))
	}
	if !reflect.DeepEqual([]string{"CURRENT.pdf", "A.pdf"}, orderedMergeInputs([]string{"A.pdf"}, "CURRENT.pdf", 0)) {
		t.Fatalf("unexpected prepend order")
	}
	if !reflect.DeepEqual([]string{"A.pdf", "CURRENT.pdf"}, orderedMergeInputs([]string{"A.pdf"}, "CURRENT.pdf", 1)) {
		t.Fatalf("unexpected append order")
	}
}

func TestValidateMergeParametersRequiresOwnedAssetIntentAndValidPosition(t *testing.T) {
	if _, err := validateMergeParameters(MergeParameters{}); err == nil {
		t.Fatal("expected empty merge asset list to be rejected")
	}
	if _, err := validateMergeParameters(MergeParameters{SourceAssetIDs: []string{"A", "A"}}); err == nil {
		t.Fatal("expected duplicate asset IDs to be rejected")
	}
	if _, err := validateMergeParameters(MergeParameters{SourceAssetIDs: []string{"A"}, CurrentDocumentPosition: intPtr(-1)}); err == nil {
		t.Fatal("expected negative current position to be rejected")
	}
	if position, err := validateMergeParameters(MergeParameters{SourceAssetIDs: []string{"A", "B"}, CurrentDocumentPosition: intPtr(1)}); err != nil || position != 1 {
		t.Fatalf("expected middle current position, got %d, %v", position, err)
	}
}

func TestMergeOrderRemainsPartOfCanonicalMaterializationIntent(t *testing.T) {
	a, err := canonicalJSON(json.RawMessage(`{"source_asset_ids":["A","B"],"current_document_position":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(json.RawMessage(`{"source_asset_ids":["B","A"],"current_document_position":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("order-sensitive merge intents must not canonicalize to the same bytes")
	}
}
