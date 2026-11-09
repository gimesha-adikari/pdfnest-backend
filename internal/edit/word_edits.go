package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	WordEditProtocolV1      = "platen_word_edits.v1"
	MaxWordEditOperations   = 1000
	MaxWordReplacementRunes = 4096
)

var (
	sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha256Pattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type WordEdit struct {
	PageNum              int    `json:"page_num"`
	ElementID            string `json:"element_id"`
	WordID               string `json:"word_id"`
	ExpectedOriginalText string `json:"expected_original_text"`
	ReplacementText      string `json:"replacement_text"`
	StyleOverride        any    `json:"style_override"`
	FitPolicy            string `json:"fit_policy"`
}

type WordEditRequest struct {
	EditProtocol    string     `json:"edit_protocol"`
	SourceTracker   string     `json:"source_tracker"`
	ExtractionJobID string     `json:"extraction_job_id"`
	BaselineDigest  string     `json:"baseline_digest"`
	Edits           []WordEdit `json:"edits"`
}

// decodeWordEditRequest keeps the v1 wire contract closed. Legacy layout JSON
// is still decoded by the pre-existing route when no word protocol fields exist.
func decodeWordEditRequest(payload []byte) (WordEditRequest, bool, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(payload, &shape); err != nil {
		return WordEditRequest{}, false, fmt.Errorf("invalid JSON request")
	}
	_, hasProtocol := shape["edit_protocol"]
	_, hasEdits := shape["edits"]
	_, hasExtraction := shape["extraction_job_id"]
	_, hasBaseline := shape["baseline_digest"]
	if !hasProtocol && !hasEdits && !hasExtraction && !hasBaseline {
		return WordEditRequest{}, false, nil
	}
	if !hasProtocol {
		return WordEditRequest{}, true, fmt.Errorf("word edit fields require edit_protocol")
	}
	var discriminator string
	if err := json.Unmarshal(shape["edit_protocol"], &discriminator); err != nil || discriminator != WordEditProtocolV1 {
		return WordEditRequest{}, true, fmt.Errorf("unsupported edit protocol")
	}
	for _, required := range []string{"source_tracker", "extraction_job_id", "baseline_digest", "edits"} {
		if _, ok := shape[required]; !ok {
			return WordEditRequest{}, true, fmt.Errorf("missing required field %s", required)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request WordEditRequest
	if err := decoder.Decode(&request); err != nil {
		return WordEditRequest{}, true, fmt.Errorf("malformed word edit request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return WordEditRequest{}, true, fmt.Errorf("trailing JSON data")
	}
	if request.EditProtocol != WordEditProtocolV1 || strings.TrimSpace(request.SourceTracker) == "" || strings.TrimSpace(request.ExtractionJobID) == "" || strings.TrimSpace(request.BaselineDigest) == "" {
		return WordEditRequest{}, true, fmt.Errorf("missing required word edit identity")
	}
	if len(request.Edits) == 0 || len(request.Edits) > MaxWordEditOperations {
		return WordEditRequest{}, true, fmt.Errorf("word edit operation count must be between 1 and %d", MaxWordEditOperations)
	}
	var edits []json.RawMessage
	if err := json.Unmarshal(shape["edits"], &edits); err != nil || len(edits) != len(request.Edits) {
		return WordEditRequest{}, true, fmt.Errorf("edits must be an array")
	}
	seen := make(map[string]bool, len(request.Edits))
	for index, raw := range edits {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return WordEditRequest{}, true, fmt.Errorf("edit %d must be an object", index)
		}
		for _, required := range []string{"page_num", "element_id", "word_id", "expected_original_text", "replacement_text", "style_override", "fit_policy"} {
			if _, ok := fields[required]; !ok {
				return WordEditRequest{}, true, fmt.Errorf("edit %d is missing %s", index, required)
			}
		}
		if !bytes.Equal(bytes.TrimSpace(fields["style_override"]), []byte("null")) {
			return WordEditRequest{}, true, fmt.Errorf("edit %d style_override is not supported", index)
		}
		e := request.Edits[index]
		if e.PageNum < 1 || strings.TrimSpace(e.ElementID) == "" || strings.TrimSpace(e.WordID) == "" || e.ExpectedOriginalText == "" || e.FitPolicy != "preserve_neighbors" {
			return WordEditRequest{}, true, fmt.Errorf("edit %d has invalid identity or fit policy", index)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", e.PageNum, e.ElementID, e.WordID)
		if seen[key] {
			return WordEditRequest{}, true, fmt.Errorf("duplicate word edit target")
		}
		seen[key] = true
		if utf8.RuneCountInString(e.ReplacementText) > MaxWordReplacementRunes {
			return WordEditRequest{}, true, fmt.Errorf("edit %d replacement exceeds %d Unicode scalar values", index, MaxWordReplacementRunes)
		}
	}
	return request, true, nil
}

func validateWordEditsAgainstBaseline(request WordEditRequest, extraction *WorkerJobRecord) error {
	if extraction == nil || extraction.ID != request.ExtractionJobID || extraction.JobType != "editor_extract" || extraction.Status != "succeeded" {
		return fmt.Errorf("extraction baseline is unavailable or incomplete")
	}
	payload := extraction.Payload
	if payload == nil || payload["source_key"] != request.SourceTracker || payload["consumer"] != "general_editor" || payload["ocr_v2"] != true {
		return fmt.Errorf("extraction baseline does not belong to this General Editor source")
	}
	baseline, ok := extraction.Result["word_edit_baseline"].(map[string]any)
	if !ok || baseline["extraction_job_id"] != request.ExtractionJobID || baseline["digest"] != request.BaselineDigest {
		return fmt.Errorf("extraction baseline digest is stale or invalid")
	}
	sourceDigest, sourceDigestOK := baseline["source_sha256"].(string)
	engineFingerprint, engineFingerprintOK := baseline["engine_fingerprint"].(string)
	algorithmVersion, algorithmVersionOK := baseline["algorithm_version"].(string)
	if !sourceDigestOK || !sha256HexPattern.MatchString(sourceDigest) || !engineFingerprintOK || !sha256Pattern.MatchString(engineFingerprint) || !algorithmVersionOK || algorithmVersion != "general_editor_word_baseline.v1" {
		return fmt.Errorf("extraction baseline identity is incomplete")
	}
	if extraction.Result["schema_version"] != "ocr_v2_editor_layout.v1" {
		return fmt.Errorf("extraction layout schema is unsupported")
	}

	pages, ok := extraction.Result["pages"].([]any)
	if !ok {
		return fmt.Errorf("extraction baseline contains no pages")
	}
	type target struct {
		text  string
		count int
	}
	targets := make(map[string]target)
	pageNumbers := make(map[int]bool)
	elementIdentities := make(map[string]bool)
	for _, rawPage := range pages {
		page, ok := rawPage.(map[string]any)
		if !ok {
			continue
		}
		pageNum := positiveJSONInt(page["page_num"])
		if pageNum == 0 || pageNumbers[pageNum] {
			return fmt.Errorf("extraction baseline has duplicate or invalid page identities")
		}
		pageNumbers[pageNum] = true
		elements, _ := page["elements"].([]any)
		for _, rawElement := range elements {
			element, ok := rawElement.(map[string]any)
			if !ok {
				continue
			}
			elementID, _ := element["id"].(string)
			words, _ := element["word_geometry"].([]any)
			identity := fmt.Sprintf("%d\x00%s", pageNum, elementID)
			if elementID == "" || elementIdentities[identity] {
				return fmt.Errorf("extraction baseline has duplicate or invalid element identities")
			}
			elementIdentities[identity] = true
			if len(words) == 0 {
				continue
			}
			wordIDs, ok := element["word_ids"].([]any)
			if !ok || len(wordIDs) != len(words) {
				return fmt.Errorf("extraction baseline word identities are inconsistent")
			}
			wordGeometryIDs := make([]string, 0, len(words))
			for _, rawWord := range words {
				word, ok := rawWord.(map[string]any)
				if !ok {
					continue
				}
				wordID, _ := word["id"].(string)
				text, _ := word["text"].(string)
				wordGeometryIDs = append(wordGeometryIDs, wordID)
				if pageNum == 0 || elementID == "" || wordID == "" {
					continue
				}
				key := fmt.Sprintf("%d\x00%s\x00%s", pageNum, elementID, wordID)
				found := targets[key]
				found.text = text
				found.count++
				targets[key] = found
			}
			for index, id := range wordGeometryIDs {
				if index >= len(wordIDs) || wordIDs[index] != id {
					return fmt.Errorf("extraction baseline word identities are inconsistent")
				}
			}
		}
	}
	for _, edit := range request.Edits {
		key := fmt.Sprintf("%d\x00%s\x00%s", edit.PageNum, edit.ElementID, edit.WordID)
		found, ok := targets[key]
		if !ok || found.count != 1 {
			return fmt.Errorf("word edit target is absent or ambiguous")
		}
		if found.text != edit.ExpectedOriginalText {
			return fmt.Errorf("expected original text does not match extraction baseline")
		}
	}
	return nil
}

func positiveJSONInt(value any) int {
	switch number := value.(type) {
	case float64:
		if number >= 1 && number == float64(int(number)) {
			return int(number)
		}
	case int:
		if number >= 1 {
			return number
		}
	case json.Number:
		var parsed int
		if _, err := fmt.Sscan(number.String(), &parsed); err == nil && parsed >= 1 {
			return parsed
		}
	}
	return 0
}
