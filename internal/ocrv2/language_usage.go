package ocrv2

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"pdfnest-backend/config"
)

// RecordLanguageSelection is deliberately best-effort.  OCR job admission
// and execution must remain available when telemetry persistence is down.
func RecordLanguageSelection(owner string, language string, correction bool) {
	if config.DB == nil || strings.EqualFold(strings.TrimSpace(language), "auto") {
		return
	}
	codes := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(language)), func(r rune) bool { return r == '+' || r == ',' || r == ' ' || r == '\t' })
	if len(codes) == 0 {
		return
	}
	set := append([]string(nil), codes...)
	sort.Strings(set)
	setValue := strings.Join(set, "+")
	ownerKey := strings.TrimSpace(owner)
	if ownerKey == "" {
		ownerKey = "anonymous"
	}
	now := time.Now().UTC()
	for _, code := range set {
		row := config.OCRLanguageUsage{}
		query := config.DB.Where("owner_key = ? AND language_code = ? AND language_set = ?", ownerKey, code, setValue).First(&row)
		if query.Error != nil {
			row = config.OCRLanguageUsage{ID: uuid.NewString(), OwnerKey: ownerKey, LanguageCode: code, LanguageSet: setValue}
		}
		row.ManualSelectionCount++
		if correction {
			row.ManualCorrectionCount++
		}
		row.LastUsedAt = now
		_ = config.DB.Save(&row).Error

		if ownerKey != "global" {
			global := config.OCRLanguageUsage{}
			if config.DB.Where("owner_key = ? AND language_code = ? AND language_set = ?", "global", code, setValue).First(&global).Error != nil {
				global = config.OCRLanguageUsage{ID: uuid.NewString(), OwnerKey: "global", LanguageCode: code, LanguageSet: setValue}
			}
			global.ManualSelectionCount++
			global.LastUsedAt = now
			_ = config.DB.Save(&global).Error
		}
	}
}

// LanguageUsageRanking returns only numeric owner/global priors.  It is an
// optional hint for bounded AUTO probing: OCR evidence remains authoritative,
// and a storage failure returns no hint rather than blocking a job.
func LanguageUsageRanking(owner string) map[string]float64 {
	if config.DB == nil {
		return nil
	}
	ownerKey := strings.TrimSpace(owner)
	if ownerKey == "" {
		ownerKey = "anonymous"
	}
	var rows []config.OCRLanguageUsage
	if err := config.DB.Where("owner_key IN ?", []string{ownerKey, "global"}).Find(&rows).Error; err != nil {
		return nil
	}
	ranking := make(map[string]float64)
	for _, row := range rows {
		// Owner history is stronger than the aggregate prior. Corrections are
		// intentionally stronger than ordinary manual selections.
		weight := float64(row.ManualSelectionCount) + float64(row.ManualCorrectionCount)*3
		if row.OwnerKey == "global" {
			weight *= 0.25
		}
		if weight > 0 {
			ranking[row.LanguageCode] += weight
		}
	}
	return ranking
}
