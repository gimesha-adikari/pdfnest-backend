package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/vdm"
)

// CommandName is the closed set of server-derived Studio VDM mutations.
type CommandName string

const (
	CommandRotatePage       CommandName = "rotate_page"
	CommandDeletePages      CommandName = "delete_pages"
	CommandReorderPages     CommandName = "reorder_pages"
	CommandDuplicatePages   CommandName = "duplicate_pages"
	CommandInsertBlankPages CommandName = "insert_blank_pages"
	CommandCropPage         CommandName = "crop_page"
	CommandUpdateMetadata   CommandName = "update_metadata"
)

const (
	maxDuplicateCopies = 10
	maxBlankPageCount  = 50
)

// ExecuteCommandRequest intentionally contains no caller-supplied VDM.
type ExecuteCommandRequest struct {
	BaseVersionID  uuid.UUID       `json:"base_version_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Operation      CommandName     `json:"operation"`
	Parameters     json.RawMessage `json:"parameters"`
}

type RotatePageParameters struct {
	PageIDs      []string `json:"page_ids"`
	DeltaDegrees int      `json:"delta_degrees"`
}

type DeletePagesParameters struct {
	PageIDs []string `json:"page_ids"`
}

type ReorderPagesParameters struct {
	PageIDs []string `json:"page_ids"`
}

type DuplicatePagesParameters struct {
	PageIDs []string `json:"page_ids"`
	Copies  int      `json:"copies"`
}

type InsertBlankPagesParameters struct {
	Position int `json:"position"`
	Count    int `json:"count"`
}

// CropPageParameters uses the VDM's native [llx, lly, urx, ury] page-space
// convention. The same box may be applied to multiple selected pages when it
// fits each page's authoritative dimensions.
type CropPageParameters struct {
	PageIDs []string  `json:"page_ids"`
	CropBox []float64 `json:"crop_box"`
}

// UpdateMetadataParameters mirrors the fields supported by the existing V1
// metadata reader/writer. Empty values clear the corresponding field.
type UpdateMetadataParameters struct {
	Title    string `json:"title"`
	Author   string `json:"author"`
	Subject  string `json:"subject"`
	Keywords string `json:"keywords"`
}

// OperationCoordinator dispatches typed commands and derives authoritative VDM state.
type OperationCoordinator interface {
	Execute(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req ExecuteCommandRequest) (*ApplyOperationResult, error)
}

type studioOperationCoordinator struct {
	repo Repository
}

func NewOperationCoordinator(repo Repository) OperationCoordinator {
	return &studioOperationCoordinator{repo: repo}
}

func (c *studioOperationCoordinator) Execute(
	ctx context.Context,
	sessionID uuid.UUID,
	ident identity.Identity,
	req ExecuteCommandRequest,
) (*ApplyOperationResult, error) {
	if req.BaseVersionID == uuid.Nil || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 {
		return nil, ErrInvalidCommand
	}

	mutation := operationMutation{
		BaseVersionID:  req.BaseVersionID,
		IdempotencyKey: req.IdempotencyKey,
		OperationName:  string(req.Operation),
		IsMaterialized: false,
	}

	switch req.Operation {
	case CommandRotatePage:
		var params RotatePageParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.PageIDs) == 0 || params.DeltaDegrees == 0 || params.DeltaDegrees%90 != 0 || params.DeltaDegrees < -270 || params.DeltaDegrees > 270 {
			return nil, ErrInvalidCommand
		}
		if err := validateUniquePageIDs(params.PageIDs); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), params.PageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			index, err := requirePages(base, params.PageIDs)
			if err != nil {
				return nil, err
			}
			for _, pageID := range params.PageIDs {
				page := &base.Pages[index[pageID]]
				page.Rotation = normalizeRotation(page.Rotation + params.DeltaDegrees)
			}
			return base, nil
		})

	case CommandDeletePages:
		var params DeletePagesParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.PageIDs) == 0 {
			return nil, ErrInvalidCommand
		}
		if err := validateUniquePageIDs(params.PageIDs); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), params.PageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			if _, err := requirePages(base, params.PageIDs); err != nil {
				return nil, err
			}
			if len(params.PageIDs) >= len(base.Pages) {
				return nil, ErrCannotDeleteAll
			}
			deleted := make(map[string]struct{}, len(params.PageIDs))
			for _, pageID := range params.PageIDs {
				deleted[pageID] = struct{}{}
			}
			pages := make([]vdm.PageDescriptor, 0, len(base.Pages)-len(deleted))
			for _, page := range base.Pages {
				if _, remove := deleted[page.PageID]; !remove {
					pages = append(pages, page)
				}
			}
			base.Pages = pages
			base.PageCount = len(pages)
			return base, nil
		})

	case CommandReorderPages:
		var params ReorderPagesParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.PageIDs) == 0 {
			return nil, ErrInvalidCommand
		}
		if err := validateUniquePageIDs(params.PageIDs); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), params.PageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			if len(params.PageIDs) != len(base.Pages) {
				return nil, ErrInvalidPageOrder
			}
			index, err := requirePages(base, params.PageIDs)
			if err != nil {
				return nil, ErrInvalidPageOrder
			}
			pages := make([]vdm.PageDescriptor, len(params.PageIDs))
			for i, pageID := range params.PageIDs {
				pages[i] = base.Pages[index[pageID]]
			}
			base.Pages = pages
			return base, nil
		})

	case CommandDuplicatePages:
		var params DuplicatePagesParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.PageIDs) == 0 || params.Copies < 1 || params.Copies > maxDuplicateCopies {
			return nil, ErrInvalidCommand
		}
		if err := validateUniquePageIDs(params.PageIDs); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), params.PageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			if _, err := requirePages(base, params.PageIDs); err != nil {
				return nil, err
			}
			selected := make(map[string]struct{}, len(params.PageIDs))
			for _, pageID := range params.PageIDs {
				selected[pageID] = struct{}{}
			}
			pages := make([]vdm.PageDescriptor, 0, len(base.Pages)+(len(params.PageIDs)*params.Copies))
			for _, page := range base.Pages {
				pages = append(pages, page)
				if _, duplicate := selected[page.PageID]; !duplicate {
					continue
				}
				for i := 0; i < params.Copies; i++ {
					copyPage := clonePageDescriptor(page)
					parentID := page.PageID
					copyPage.PageID = uuid.NewString()
					copyPage.ParentPageID = &parentID
					pages = append(pages, copyPage)
				}
			}
			base.Pages = pages
			base.PageCount = len(pages)
			return base, nil
		})

	case CommandInsertBlankPages:
		var params InsertBlankPagesParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || params.Position < 0 || params.Count < 1 || params.Count > maxBlankPageCount {
			return nil, ErrInvalidCommand
		}
		mutation.Parameters, _ = json.Marshal(params)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			if params.Position > len(base.Pages) {
				return nil, ErrInvalidCommand
			}
			dimensions := adjacentDimensions(base.Pages, params.Position)
			if dimensions == nil {
				return nil, ErrBlankDimensions
			}
			blanks := make([]vdm.PageDescriptor, params.Count)
			for i := range blanks {
				dims := *dimensions
				blanks[i] = vdm.PageDescriptor{
					PageID:     uuid.NewString(),
					IsBlank:    true,
					Dimensions: &dims,
					Rotation:   0,
					Overlays:   []vdm.Overlay{},
				}
			}
			pages := make([]vdm.PageDescriptor, 0, len(base.Pages)+len(blanks))
			pages = append(pages, base.Pages[:params.Position]...)
			pages = append(pages, blanks...)
			pages = append(pages, base.Pages[params.Position:]...)
			base.Pages = pages
			base.PageCount = len(pages)
			return base, nil
		})

	case CommandCropPage:
		var params CropPageParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.PageIDs) == 0 || len(params.CropBox) != 4 {
			return nil, ErrInvalidCommand
		}
		if err := validateUniquePageIDs(params.PageIDs); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), params.PageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			index, err := requirePages(base, params.PageIDs)
			if err != nil {
				return nil, err
			}
			for _, pageID := range params.PageIDs {
				page := &base.Pages[index[pageID]]
				if err := validateCropBox(page, params.CropBox); err != nil {
					return nil, err
				}
				page.CropBox = append([]float64(nil), params.CropBox...)
			}
			return base, nil
		})

	case CommandUpdateMetadata:
		var params UpdateMetadataParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil {
			return nil, ErrInvalidCommand
		}
		if err := validateMetadataParameters(&params); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			metadata := make(map[string]string, len(base.Metadata)+4)
			for key, value := range base.Metadata {
				metadata[key] = value
			}
			metadata["Title"] = strings.TrimSpace(params.Title)
			metadata["Author"] = strings.TrimSpace(params.Author)
			metadata["Subject"] = strings.TrimSpace(params.Subject)
			metadata["Keywords"] = strings.TrimSpace(params.Keywords)
			base.Metadata = metadata
			return base, nil
		})

	default:
		return nil, ErrUnknownCommand
	}
}

func validateCropBox(page *vdm.PageDescriptor, cropBox []float64) error {
	if page == nil || len(cropBox) != 4 || page.Dimensions == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
		return ErrInvalidCropBox
	}
	for _, value := range cropBox {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidCropBox
		}
	}
	llx, lly, urx, ury := cropBox[0], cropBox[1], cropBox[2], cropBox[3]
	if llx < 0 || lly < 0 || urx <= llx || ury <= lly || urx > page.Dimensions.Width || ury > page.Dimensions.Height {
		return ErrInvalidCropBox
	}
	return nil
}

func validateMetadataParameters(params *UpdateMetadataParameters) error {
	if params == nil {
		return ErrInvalidMetadata
	}
	for _, value := range []string{params.Title, params.Author, params.Subject, params.Keywords} {
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return ErrInvalidMetadata
		}
	}
	return nil
}

func decodeStrictParameters(raw json.RawMessage, target interface{}) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ErrInvalidCommand
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidCommand
	}
	return nil
}

func validateUniquePageIDs(pageIDs []string) error {
	seen := make(map[string]struct{}, len(pageIDs))
	for _, pageID := range pageIDs {
		if pageID == "" {
			return ErrInvalidCommand
		}
		if _, exists := seen[pageID]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicatePageID, pageID)
		}
		seen[pageID] = struct{}{}
	}
	return nil
}

func requirePages(model *vdm.DocumentModel, pageIDs []string) (map[string]int, error) {
	index := make(map[string]int, len(model.Pages))
	for i, page := range model.Pages {
		index[page.PageID] = i
	}
	for _, pageID := range pageIDs {
		if _, exists := index[pageID]; !exists {
			return nil, fmt.Errorf("%w: %s", ErrCommandPageNotFound, pageID)
		}
	}
	return index, nil
}

func normalizeRotation(rotation int) int {
	rotation %= 360
	if rotation < 0 {
		rotation += 360
	}
	return rotation
}

func clonePageDescriptor(page vdm.PageDescriptor) vdm.PageDescriptor {
	clone := page
	if page.SourceAssetID != nil {
		assetID := *page.SourceAssetID
		clone.SourceAssetID = &assetID
	}
	if page.ParentPageID != nil {
		parentID := *page.ParentPageID
		clone.ParentPageID = &parentID
	}
	if page.Dimensions != nil {
		dimensions := *page.Dimensions
		clone.Dimensions = &dimensions
	}
	clone.CropBox = append([]float64(nil), page.CropBox...)
	clone.Overlays = make([]vdm.Overlay, len(page.Overlays))
	for i, overlay := range page.Overlays {
		clone.Overlays[i] = overlay
		clone.Overlays[i].Rect = append([]float64(nil), overlay.Rect...)
		clone.Overlays[i].Quads = append([]float64(nil), overlay.Quads...)
	}
	return clone
}

func adjacentDimensions(pages []vdm.PageDescriptor, position int) *vdm.Dimensions {
	if position < len(pages) && pages[position].Dimensions != nil {
		dimensions := *pages[position].Dimensions
		return &dimensions
	}
	if position > 0 && pages[position-1].Dimensions != nil {
		dimensions := *pages[position-1].Dimensions
		return &dimensions
	}
	return nil
}
