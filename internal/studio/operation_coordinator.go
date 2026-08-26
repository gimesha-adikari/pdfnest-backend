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
	CommandRotatePage          CommandName = "rotate_page"
	CommandDeletePages         CommandName = "delete_pages"
	CommandReorderPages        CommandName = "reorder_pages"
	CommandDuplicatePages      CommandName = "duplicate_pages"
	CommandInsertBlankPages    CommandName = "insert_blank_pages"
	CommandCropPage            CommandName = "crop_page"
	CommandUpdateMetadata      CommandName = "update_metadata"
	CommandUpdatePageNumbering CommandName = "update_page_numbering"
	CommandAddTextOverlay      CommandName = "add_text_overlay"
	CommandUpdateTextOverlay   CommandName = "update_text_overlay"
	CommandAddWatermark        CommandName = "add_watermark"
	CommandDeleteOverlay       CommandName = "delete_overlay"
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

// UpdatePageNumberingParameters exposes only the settings supported by the
// existing V1 Page Numbers product. The rule remains document-level state;
// generated labels are never persisted into individual page descriptors.
type UpdatePageNumberingParameters struct {
	Enabled    bool    `json:"enabled"`
	Position   string  `json:"position,omitempty"`
	FontSize   float64 `json:"font_size,omitempty"`
	FontFamily string  `json:"font_family,omitempty"`
}

// AddTextOverlayParameters is the deliberately small foundation command. The
// coordinates are native unrotated PDF points with a lower-left origin; the
// server creates the authoritative overlay identity and descriptor.
type AddTextOverlayParameters struct {
	PageID   string  `json:"page_id"`
	Text     string  `json:"text"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	FontSize float64 `json:"font_size"`
	Color    string  `json:"color"`
}

type UpdateTextOverlayParameters struct {
	PageID    string  `json:"page_id"`
	OverlayID string  `json:"overlay_id"`
	Text      string  `json:"text"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	FontSize  float64 `json:"font_size"`
	Color     string  `json:"color"`
}

// AddWatermarkParameters mirrors the V1 watermark controls while keeping the
// page scope and all placement decisions server-owned.
type AddWatermarkParameters struct {
	PageIDs  []string `json:"page_ids"`
	Kind     string   `json:"kind"`
	Text     string   `json:"text,omitempty"`
	Font     string   `json:"font,omitempty"`
	FontSize float64  `json:"font_size"`
	Rotation int      `json:"rotation"`
	Opacity  float64  `json:"opacity"`
	Position string   `json:"position"`
	AssetID  string   `json:"asset_id,omitempty"`
}

type DeleteOverlayTarget struct {
	PageID    string `json:"page_id"`
	OverlayID string `json:"overlay_id"`
}

type DeleteOverlayParameters struct {
	Targets []DeleteOverlayTarget `json:"targets"`
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

	case CommandUpdatePageNumbering:
		var params UpdatePageNumberingParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil {
			return nil, ErrInvalidCommand
		}
		mutation.Parameters, _ = json.Marshal(params)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			rule, err := derivePageNumberingRule(base.PageNumbering, &params)
			if err != nil {
				return nil, err
			}
			base.PageNumbering = rule
			return base, nil
		})

	case CommandAddTextOverlay:
		var params AddTextOverlayParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil {
			return nil, ErrInvalidCommand
		}
		if err := validateTextOverlayParameters(&params); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = []string{params.PageID}
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			index, err := requirePages(base, []string{params.PageID})
			if err != nil {
				return nil, err
			}
			page := &base.Pages[index[params.PageID]]
			if err := validateTextOverlayPlacement(page, &params); err != nil {
				return nil, err
			}
			page.Overlays = append(page.Overlays, vdm.Overlay{
				ID:       uuid.NewString(),
				Type:     string(vdm.OverlayTypeText),
				Text:     params.Text,
				Font:     "Helvetica",
				Color:    params.Color,
				FontSize: params.FontSize,
				Rect:     []float64{params.X, params.Y, 0, 0},
			})
			return base, nil
		})

	case CommandUpdateTextOverlay:
		var params UpdateTextOverlayParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil {
			return nil, ErrInvalidCommand
		}
		if err := validateTextOverlayParameters(&AddTextOverlayParameters{PageID: params.PageID, Text: params.Text, X: params.X, Y: params.Y, FontSize: params.FontSize, Color: params.Color}); err != nil {
			return nil, err
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = []string{params.PageID}
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			index, err := requirePages(base, []string{params.PageID})
			if err != nil {
				return nil, err
			}
			page := &base.Pages[index[params.PageID]]
			if err := validateTextOverlayPlacement(page, &AddTextOverlayParameters{PageID: params.PageID, Text: params.Text, X: params.X, Y: params.Y, FontSize: params.FontSize, Color: params.Color}); err != nil {
				return nil, err
			}
			found := false
			for overlayIndex := range page.Overlays {
				overlay := &page.Overlays[overlayIndex]
				if overlay.ID != params.OverlayID {
					continue
				}
				if overlay.Type != string(vdm.OverlayTypeText) {
					return nil, ErrInvalidOverlay
				}
				overlay.Text, overlay.Rect, overlay.FontSize, overlay.Color = params.Text, []float64{params.X, params.Y, 0, 0}, params.FontSize, params.Color
				found = true
				break
			}
			if !found {
				return nil, ErrInvalidOverlay
			}
			return base, nil
		})

	case CommandAddWatermark:
		var params AddWatermarkParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil {
			return nil, ErrInvalidCommand
		}
		if err := validateWatermarkParameters(&params); err != nil {
			return nil, err
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
			if params.Kind == "image" {
				sess, sessionErr := c.repo.GetSession(ctx, sessionID)
				if sessionErr != nil {
					return nil, sessionErr
				}
				asset, assetErr := c.repo.GetAsset(ctx, params.AssetID)
				if assetErr != nil {
					return nil, assetErr
				}
				if sess == nil || asset.DocumentID != sess.DocumentID || asset.AssetType != "watermark_image" || (asset.MimeType != "image/png" && asset.MimeType != "image/jpeg") {
					return nil, ErrUnauthorized
				}
			}
			for _, pageID := range params.PageIDs {
				page := &base.Pages[index[pageID]]
				rect := deriveWatermarkRect(page, &params)
				if rect == nil {
					return nil, ErrInvalidOverlay
				}
				page.Overlays = append(page.Overlays, vdm.Overlay{
					ID: uuid.NewString(), Type: string(vdm.OverlayTypeWatermark), Text: params.Text,
					Font: params.Font, FontSize: params.FontSize, Opacity: params.Opacity,
					Rotation: params.Rotation, Position: params.Position, AssetID: params.AssetID, Rect: rect,
				})
			}
			return base, nil
		})

	case CommandDeleteOverlay:
		var params DeleteOverlayParameters
		if err := decodeStrictParameters(req.Parameters, &params); err != nil || len(params.Targets) == 0 {
			return nil, ErrInvalidCommand
		}
		seen := make(map[string]struct{}, len(params.Targets))
		pageIDs := make([]string, 0, len(params.Targets))
		for _, target := range params.Targets {
			if target.PageID == "" || target.OverlayID == "" {
				return nil, ErrInvalidOverlay
			}
			key := target.PageID + "\x00" + target.OverlayID
			if _, exists := seen[key]; exists {
				return nil, ErrInvalidOverlay
			}
			seen[key] = struct{}{}
			pageIDs = append(pageIDs, target.PageID)
		}
		if err := validateUniquePageIDs(pageIDs); err != nil {
			// Multiple overlays on the same page are valid delete targets.
			pageIDs = uniqueStrings(pageIDs)
		}
		mutation.Parameters, _ = json.Marshal(params)
		mutation.TargetPageIDs = append([]string(nil), pageIDs...)
		return persistOperation(ctx, c.repo, sessionID, ident, mutation, func(base *vdm.DocumentModel) (*vdm.DocumentModel, error) {
			index, err := requirePages(base, pageIDs)
			if err != nil {
				return nil, err
			}
			for _, target := range params.Targets {
				page := &base.Pages[index[target.PageID]]
				found := false
				filtered := make([]vdm.Overlay, 0, len(page.Overlays))
				for _, overlay := range page.Overlays {
					if overlay.ID == target.OverlayID {
						found = true
						continue
					}
					filtered = append(filtered, overlay)
				}
				if !found {
					return nil, ErrInvalidOverlay
				}
				page.Overlays = filtered
			}
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

func derivePageNumberingRule(current *vdm.PageNumberingRule, params *UpdatePageNumberingParameters) (*vdm.PageNumberingRule, error) {
	if params == nil {
		return nil, ErrInvalidPageNumbering
	}
	if current == nil {
		current = &vdm.PageNumberingRule{Format: "%p", Position: "bc", FontSize: 12, FontFamily: "Helvetica", StartAt: 1}
	}
	rule := *current
	if params.Position != "" {
		rule.Position = strings.ToLower(strings.TrimSpace(params.Position))
	}
	if params.FontFamily != "" {
		rule.FontFamily = strings.TrimSpace(params.FontFamily)
	}
	if params.FontSize != 0 {
		rule.FontSize = params.FontSize
	}
	if rule.Format == "" {
		rule.Format = "%p"
	}
	if rule.StartAt == 0 {
		rule.StartAt = 1
	}
	if rule.Position == "" {
		rule.Position = "bc"
	}
	if rule.FontFamily == "" {
		rule.FontFamily = "Helvetica"
	}
	if !params.Enabled {
		rule.Enabled = false
		return &rule, nil
	}
	if !utf8.ValidString(rule.Position) || strings.IndexByte(rule.Position, 0) >= 0 || !utf8.ValidString(rule.FontFamily) || strings.IndexByte(rule.FontFamily, 0) >= 0 {
		return nil, ErrInvalidPageNumbering
	}
	if rule.FontSize < 6 || rule.FontSize > 72 || math.IsNaN(rule.FontSize) || math.IsInf(rule.FontSize, 0) {
		return nil, ErrInvalidPageNumbering
	}
	switch rule.Position {
	case "bl", "bc", "br", "tl", "tc", "tr":
	default:
		return nil, ErrInvalidPageNumbering
	}
	switch rule.FontFamily {
	case "Helvetica", "Times-Roman", "Courier":
	default:
		return nil, ErrInvalidPageNumbering
	}
	rule.Enabled = true
	rule.Format = "%p"
	rule.StartAt = 1
	rule.OmittedPageIDs = nil
	return &rule, nil
}

func validateTextOverlayParameters(params *AddTextOverlayParameters) error {
	if params == nil || params.PageID == "" || !utf8.ValidString(params.Text) || strings.IndexByte(params.Text, 0) >= 0 || strings.TrimSpace(params.Text) == "" {
		return ErrInvalidOverlay
	}
	if math.IsNaN(params.X) || math.IsInf(params.X, 0) || math.IsNaN(params.Y) || math.IsInf(params.Y, 0) || math.IsNaN(params.FontSize) || math.IsInf(params.FontSize, 0) {
		return ErrInvalidOverlay
	}
	if params.FontSize < 8 || params.FontSize > 144 {
		return ErrInvalidOverlay
	}
	if params.Color == "" {
		params.Color = "#000000"
	}
	if !strings.HasPrefix(params.Color, "#") || len(params.Color) != 7 {
		return ErrInvalidOverlay
	}
	return nil
}

func validateTextOverlayPlacement(page *vdm.PageDescriptor, params *AddTextOverlayParameters) error {
	if page == nil || page.Dimensions == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
		return ErrInvalidOverlay
	}
	if params.X < 0 || params.Y < 0 || params.X+params.FontSize > page.Dimensions.Width || params.Y+params.FontSize > page.Dimensions.Height {
		return ErrInvalidOverlay
	}
	return nil
}

func validateWatermarkParameters(params *AddWatermarkParameters) error {
	if params == nil || len(params.PageIDs) == 0 || (params.Kind != "text" && params.Kind != "image") {
		return ErrInvalidOverlay
	}
	if params.Kind == "text" && (!utf8.ValidString(params.Text) || strings.TrimSpace(params.Text) == "" || strings.IndexByte(params.Text, 0) >= 0) {
		return ErrInvalidOverlay
	}
	if params.Kind == "image" && params.AssetID == "" {
		return ErrInvalidOverlay
	}
	if params.Font == "" {
		params.Font = "Helvetica"
	}
	if params.Font != "Helvetica" && params.Font != "Times-Roman" && params.Font != "Courier" {
		return ErrInvalidOverlay
	}
	if math.IsNaN(params.FontSize) || math.IsInf(params.FontSize, 0) || params.FontSize < 10 || params.FontSize > 300 || params.Rotation < -180 || params.Rotation > 180 || math.IsNaN(params.Opacity) || math.IsInf(params.Opacity, 0) || params.Opacity < 0.05 || params.Opacity > 1 {
		return ErrInvalidOverlay
	}
	switch params.Position {
	case "tl", "tc", "tr", "cl", "cc", "cr", "bl", "bc", "br":
		return nil
	default:
		return ErrInvalidOverlay
	}
}

func deriveWatermarkRect(page *vdm.PageDescriptor, params *AddWatermarkParameters) []float64 {
	if page == nil || page.Dimensions == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
		return nil
	}
	fontPoints := math.Max(1, params.FontSize*0.4)
	width := fontPoints * math.Max(1, float64(len([]rune(params.Text)))*0.55)
	height := fontPoints
	if params.Kind == "image" {
		width = math.Max(1, params.FontSize*1.5)
		height = width
	}
	width = math.Min(width, page.Dimensions.Width)
	height = math.Min(height, page.Dimensions.Height)
	x, y := 0.0, 0.0
	switch params.Position[1] {
	case 'c':
		x = (page.Dimensions.Width - width) / 2
	case 'r':
		x = page.Dimensions.Width - width
	}
	switch params.Position[0] {
	case 't':
		y = page.Dimensions.Height - height
	case 'c':
		y = (page.Dimensions.Height - height) / 2
	}
	return []float64{math.Max(0, x), math.Max(0, y), width, height}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
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
