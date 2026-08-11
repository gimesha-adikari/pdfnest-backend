package conversion

import (
	"encoding/json"
	"io"
)

func decodeJSON(
	reader io.Reader,
	target any,
) error {
	return json.NewDecoder(reader).Decode(target)
}
