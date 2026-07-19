package markup

type Mode string

const (
	ModeManual Mode = "manual"
	ModeSmart  Mode = "smart"
	ModeText   Mode = "text"
	ModeOCR    Mode = "ocr"
)

type Action string

const (
	ActionHighlight Action = "highlight"
	ActionUnderline Action = "underline"
	ActionStrikeout Action = "strikeout"
)

type Box struct {
	ID     string  `json:"id,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Page   int     `json:"page"`
	Color  string  `json:"color,omitempty"`
}

// HighlightBox Keep separate names so you do not have to touch old call sites too much.
type HighlightBox = Box
type UnderlineBox = Box
type StrikeoutBox = Box

type JobSubmissionResponse struct {
	Success   bool   `json:"success"`
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	QueueName string `json:"queue_name"`
}

type workerJobSubmission struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	QueueName string `json:"queue_name"`
}

type workerJobRecord struct {
	ID              string         `json:"id"`
	JobType         string         `json:"job_type"`
	Status          string         `json:"status"`
	Progress        int            `json:"progress"`
	Message         string         `json:"message"`
	Result          map[string]any `json:"result"`
	Error           string         `json:"error"`
	CancelRequested bool           `json:"cancel_requested"`
}
