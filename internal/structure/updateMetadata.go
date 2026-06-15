package structure

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Helper function to detect and decode UTF-16 Byte Order Marks (BOM) in PDF strings
func decodePdfString(s string) string {
	b := []byte(s)

	// Check for UTF-16 Big Endian BOM (FE FF) - Standard for PDFs
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u16 := make([]uint16, (len(b)-2)/2)
		for i := 0; i < len(u16); i++ {
			u16[i] = binary.BigEndian.Uint16(b[2+(i*2):])
		}
		return string(utf16.Decode(u16))
	}

	// Check for UTF-16 Little Endian BOM (FF FE) - Rare, but good to handle
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u16 := make([]uint16, (len(b)-2)/2)
		for i := 0; i < len(u16); i++ {
			u16[i] = binary.LittleEndian.Uint16(b[2+(i*2):])
		}
		return string(utf16.Decode(u16))
	}

	return s
}

func (s *structureService) UpdateMetadataPDF(inputPath string, metadata map[string]string, password string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "metadata-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	if password != "" {
		config.UserPW = password
		config.OwnerPW = password
	}

	err := api.AddPropertiesFile(inputPath, outputPath, metadata, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}

func (s *structureService) GetMetadataPDF(inputPath string, password string) (map[string]string, error) {
	config := model.NewDefaultConfiguration()

	if password != "" {
		config.UserPW = password
		config.OwnerPW = password
	}

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, err
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {

		}
	}(file)

	resultMap := make(map[string]string)

	propsList, err := api.Properties(file, config)
	if err == nil {
		for _, line := range propsList {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			idx := strings.Index(line, ":")
			if idx == -1 {
				idx = strings.Index(line, "=")
			}
			if idx == -1 {
				continue
			}

			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = strings.Trim(val, `"'()`)

			// Decode UTF-16 properties if present
			switch strings.ToLower(key) {
			case "title":
				resultMap["title"] = decodePdfString(val)
			case "author":
				resultMap["author"] = decodePdfString(val)
			case "subject":
				resultMap["subject"] = decodePdfString(val)
			case "keywords":
				resultMap["keywords"] = decodePdfString(val)
			}
		}
	}

	if len(resultMap) == 0 {
		_, _ = file.Seek(0, 0)
		scanner := bufio.NewScanner(file)

		for scanner.Scan() {
			line := scanner.Text()

			if strings.Contains(line, "/Title") || strings.Contains(line, "dc:title") {
				if val := extractPdfTagValue(line, "/Title", "dc:title"); val != "" && resultMap["title"] == "" {
					resultMap["title"] = val
				}
			}
			if strings.Contains(line, "/Author") || strings.Contains(line, "dc:creator") {
				if val := extractPdfTagValue(line, "/Author", "dc:creator"); val != "" && resultMap["author"] == "" {
					resultMap["author"] = val
				}
			}
			if strings.Contains(line, "/Subject") || strings.Contains(line, "dc:description") {
				if val := extractPdfTagValue(line, "/Subject", "dc:description"); val != "" && resultMap["subject"] == "" {
					resultMap["subject"] = val
				}
			}
			if strings.Contains(line, "/Keywords") || strings.Contains(line, "pdf:Keywords") {
				if val := extractPdfTagValue(line, "/Keywords", "pdf:Keywords"); val != "" && resultMap["keywords"] == "" {
					resultMap["keywords"] = val
				}
			}
		}
	}

	fmt.Printf("[METADATA DEBUG] Consolidated Output Map: %+v\n", resultMap)
	return resultMap, nil
}

func extractPdfTagValue(line, pdfTag, xmlTag string) string {
	var val string

	if strings.Contains(line, pdfTag) {
		idx := strings.Index(line, pdfTag)
		sub := line[idx+len(pdfTag):]
		start := strings.Index(sub, "(")

		if start != -1 {
			subAfterStart := sub[start+1:]
			end := strings.Index(subAfterStart, ")")
			if end != -1 {
				val = subAfterStart[:end]
			}
		}
	} else if strings.Contains(line, xmlTag) {
		startTag := fmt.Sprintf("<%s>", xmlTag)
		endTag := fmt.Sprintf("</%s>", xmlTag)

		if strings.Contains(line, startTag) && strings.Contains(line, endTag) {
			sIdx := strings.Index(line, startTag) + len(startTag)
			eIdx := strings.Index(line, endTag)
			val = line[sIdx:eIdx]

			if strings.Contains(val, "<rdf:li>") {
				liStart := strings.Index(val, "<rdf:li>") + 8
				liSub := val[liStart:]
				if closeBracket := strings.Index(liSub, ">"); closeBracket != -1 && strings.Contains(val, "</rdf:li>") {
					liSub = liSub[closeBracket+1:]
				}
				liEnd := strings.Index(liSub, "</rdf:li>")
				if liEnd != -1 {
					val = liSub[:liEnd]
				}
			}
		}
	}

	return decodePdfString(strings.TrimSpace(val))
}
