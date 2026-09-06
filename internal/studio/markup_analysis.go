package studio

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/structure"
	"pdfnest-backend/internal/studio/vdm"
)

// MarkupAnalysisProvider is the read-only Studio-owned analysis seam. It is
// intentionally separate from job submission so readiness never creates a
// Studio version or exposes a standalone tool route to the browser.
type MarkupAnalysisProvider interface {
	AnalyzeMarkup(context.Context, uuid.UUID, identity.Identity) (*structure.PDFAnalysis, error)
}

type studioMarkupAnalysisProvider struct {
	repo     Repository
	analyzer interface {
		AnalyzePDF(string, string) (*structure.PDFAnalysis, error)
	}
}

func NewMarkupAnalysisProvider(repo Repository, analyzer interface {
	AnalyzePDF(string, string) (*structure.PDFAnalysis, error)
}) MarkupAnalysisProvider {
	return &studioMarkupAnalysisProvider{repo: repo, analyzer: analyzer}
}

func (p *studioMarkupAnalysisProvider) AnalyzeMarkup(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*structure.PDFAnalysis, error) {
	sess, err := p.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}
	version, err := p.repo.GetVersion(ctx, sess.ActiveVersionID)
	if err != nil {
		return nil, err
	}
	var model vdm.DocumentModel
	if err := json.Unmarshal(version.VirtualModel, &model); err != nil {
		return nil, fmt.Errorf("decode Studio VDM: %w", err)
	}
	var sourceAssetID string
	for _, page := range model.Pages {
		if !page.IsBlank && page.SourceAssetID != nil && *page.SourceAssetID != "" {
			sourceAssetID = *page.SourceAssetID
			break
		}
	}
	if sourceAssetID == "" {
		return nil, fmt.Errorf("Studio document has no source PDF")
	}
	asset, err := p.repo.GetAsset(ctx, sourceAssetID)
	if err != nil {
		return nil, err
	}
	if asset.DocumentID != sess.DocumentID || (asset.AssetType != "source_pdf" && asset.AssetType != "job_result" && asset.AssetType != "materialized" && asset.AssetType != "snapshot") {
		return nil, ErrUnauthorized
	}
	path, cleanup, err := storage.ResolveObject(ctx, asset.R2Key, "studio-markup-analysis", ".pdf")
	if err != nil {
		return nil, fmt.Errorf("resolve Studio source PDF: %w", err)
	}
	defer cleanup()
	return p.analyzer.AnalyzePDF(path, "")
}

var _ MarkupAnalysisProvider = (*studioMarkupAnalysisProvider)(nil)
