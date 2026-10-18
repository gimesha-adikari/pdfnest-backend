package conversion

import (
	"os"
	"strconv"
)

// The test executable stands in for the server executable in subprocess tests.
// Exit before testing's flag parsing or any other package's TestMain setup.
func init() {
	if len(os.Args) > 1 && os.Args[1] == codeHighlightWorkerCommand {
		if marker := os.Getenv("PDFNEST_TEST_HIGHLIGHT_PID"); marker != "" {
			if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				os.Exit(1)
			}
		}
	}

	if handled, err := RunCodeHighlightWorker(os.Args[1:]); handled {
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}
