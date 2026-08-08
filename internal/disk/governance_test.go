package disk

import (
	"errors"
	"os"
	"testing"
)

func TestCheckAvailableSpace_SufficientSpace(t *testing.T) {
	// 1 KB requirement should pass on normal filesystem
	err := CheckAvailableSpace(os.TempDir(), 1024)
	if err != nil {
		t.Fatalf("Expected nil error for 1KB space check, got: %v", err)
	}
}

func TestCheckAvailableSpace_ExcessiveSpaceRequirement(t *testing.T) {
	// 100 Petabytes requirement should trigger ErrInsufficientStorage
	var excessiveBytes uint64 = 100 * 1024 * 1024 * 1024 * 1024 * 1024
	err := CheckAvailableSpace(os.TempDir(), excessiveBytes)
	if err == nil {
		t.Fatalf("Expected ErrInsufficientStorage for 100 PB space requirement, got nil")
	}
	if !errors.Is(err, ErrInsufficientStorage) {
		t.Errorf("Expected ErrInsufficientStorage, got: %v", err)
	}
}

func TestEstimateRequiredSpace(t *testing.T) {
	req := EstimateRequiredSpace(10*1024*1024, 15.0, 100*1024*1024)
	expected := uint64(150*1024*1024 + 100*1024*1024)
	if req != expected {
		t.Errorf("Expected %d bytes, got %d", expected, req)
	}
}
