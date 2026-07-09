package helper

import (
	"strconv"
	"strings"
)

func countSelectedPages(selection string) int {
	count := 0

	parts := strings.Split(selection, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)

		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			r := strings.Split(part, "-")

			if len(r) != 2 {
				continue
			}

			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))

			if err1 != nil || err2 != nil || end < start {
				continue
			}

			count += end - start + 1
		} else {
			if _, err := strconv.Atoi(part); err == nil {
				count++
			}
		}
	}

	return count
}
