package content

import (
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
)

func SeedSiteContent() {
	var homeCount int64
	config.DB.Model(&models.HomePageContent{}).Count(&homeCount)
	if homeCount == 0 {
		homeContent := models.HomePageContent{
			ID:                    1,
			HeroBadgeGuest:        "Professional PDF Workspace",
			HeroBadgeFree:         "Free Plan Active",
			HeroBadgePro:          "Pro Workspace Active",
			HeroWelcomeBack:       "Welcome Back",
			HeroTitleGuest:        "PDF Workspace",
			HeroTitlePro:          "Pro Workspace",
			HeroSubtitleGuest:     "Edit, convert, secure, and organize PDFs online with advanced, cloud-native processing tools.",
			HeroSubtitleGuestBold: "Start free. Upgrade anytime.",

			AuthBannerProAccess:  "Access: All Premium Workspaces & Advanced Tools",
			AuthBannerFreeUsage:  "Daily Usage: 5 operations remaining today",
			AuthBannerFreeAction: "Upgrade to Pro",

			Feature1Title:       "Free Tier Included",
			Feature1Description: "Access baseline document utilities instantly with zero upfront costs or mandatory payment thresholds.",
			Feature2Title:       "Pro Ecosystem",
			Feature2Description: "Unlock high-performance processing architectures, interactive digital canvas features, and massive filesizes.",
			Feature3Title:       "Isolated Sandbox",
			Feature3Description: "Secure corporate sandboxes compile your parameters layout grids. Data clears instantly post compilation.",

			SearchPlaceholder:      "Search tool modules (e.g., merge, watermark, encrypt)...",
			SearchScopeSuffix:      "tools matching search matrix scope",
			SearchEmptyTitle:       "No structural modules matched",
			SearchEmptyDescription: "Try checking code spelling tags or clear filters.",

			PopularToolTitle:       "Merge PDF Documents Collectively",
			PopularToolDescription: "Combine separate structural files into a clean compound container setup natively in seconds without data compression loss.",
			PopularToolAction:      "Open Tool Module",

			CategoryEditingTitle:  "PDF Document Architecture Editing",
			CategoryEditingDesc:   "Modify structural parameters and compile native document elements layout grids.",
			CategoryConvertTitle:  "Conversion Modules",
			CategoryConvertDesc:   "Change container formats.",
			CategorySecurityTitle: "High-Grade Security",
			CategorySecurityDesc:  "Attach high-fidelity cipher authorization signatures securely.",
		}
		config.DB.Create(&homeContent)
	}

	var subCount int64
	config.DB.Model(&models.SubscribePageContent{}).Count(&subCount)
	if subCount == 0 {
		subContent := models.SubscribePageContent{
			ID:                1,
			HeroBadge:         "Value Upgrades",
			HeroTitle:         "Unlock More With",
			HeroTitleGradient: "PDFNest Pro Ecosystem",
			HeroSubtitle:      "Everything you need to edit, convert, organize and secure PDFs. Start free. Upgrade when your workflow grows.",

			PremiumSectionTitle: "Premium Features Built For Production Workflows",
			StudioTitle:         "Virtual Document Studio",
			StudioDescription:   "Manage PDF workflows from a single workspace.",
			StudioBulletPoints:  "Edit pages,Watermarks,Metadata,Security controls,Multi-step workflows",
			CanvasTitle:         "Interactive Canvas",
			CanvasDescription:   "Create PDFs visually.",
			CanvasBulletPoints:  "Drag and drop,Custom layouts,Multiple images,Professional exports",
			SpeedTitle:          "Faster Processing",
			SpeedDescription:    "Built for power users.",
			SpeedBulletPoints:   "Priority queues,Larger limits,Faster operations,Premium tools",

			FreeTitle:        "Free",
			FreePrice:        "0",
			FreeSubtitle:     "Perfect for occasional use",
			FreeBulletPoints: "Core PDF tools,Basic OCR,Secure processing,5 operations/day",

			PlusTitle:        "Plus",
			PlusPrice:        "9",
			PlusSubtitle:     "For active users",
			PlusBulletPoints: "Everything in Free,50 operations/day,Full OCR,Priority processing,Larger file support",

			ProTitle:        "Pro",
			ProPrice:        "29",
			ProSubtitle:     "For professionals",
			ProBulletPoints: "Everything in Plus,500 operations/day,Virtual Studio Workspace,Interactive visual canvas,Premium Workspace layers",

			SecurityTitle:    "Your files stay completely private",
			SecuritySubtitle: "Document security is integrated right into the core architecture loops.",
			SecurityTags:     "Temporary processing,Secure transfers,Automatic cleanup,No permanent storage",

			CtaGuestTitle:   "Create a free account and start using PDFNest today.",
			CtaFreeTitle:    "Need more power?",
			CtaFreeSubtitle: "Upgrade to Plus or Pro to expand daily thresholds.",
			CtaPlusTitle:    "Ready for Studio and Interactive Canvas?",
			CtaProTitle:     "You already have full access.",
			CtaProSubtitle:  "Your account profile holds absolute execution clearances.",

			FaqsJSON: `[
				{"q": "Is PDFNest free?", "a": "Yes! Our Free Plan gives you access to core PDF utility tools with a baseline allocation of 5 operations per day entirely free."},
				{"q": "What does Plus include?", "a": "Plus scales your volume limits up to 50 operations per day, unlocks full high-quality OCR extraction, guarantees priority server processing slots, and handles much larger file structures."},
				{"q": "What does Pro include?", "a": "Pro is our ultimate tier. It includes 500 daily operations, complete access to our multi-step Virtual Document Studio workspace, and the visual layout Interactive Canvas."},
				{"q": "Can I cancel anytime?", "a": "Absolutely. You are never locked into long agreements. You can upgrade, downgrade, or cancel your active billing status inside your settings panel at any point."},
				{"q": "Are my files stored?", "a": "Never. Privacy is paramount. Files are processed within private, temporary runtime sandboxes and permanently wiped immediately after processing."}
			]`,
		}
		config.DB.Create(&subContent)
	}

	var count int64
	config.DB.Model(&models.AboutPageContent{}).Count(&count)
	if count == 0 {
		initialAbout := models.AboutPageContent{
			ID:                  1,
			HeroTag:             "About PDFNest",
			HeroTitle:           "Built For Modern PDF Workflows",
			HeroDescription:     "PDFNest combines powerful PDF tools, visual workspaces, and secure cloud processing into a single platform. Whether you're editing a document, creating a portfolio, preparing reports, or managing complex PDF workflows, PDFNest helps you work faster and smarter.",
			StatsJson:           `[{"value":"37+","label":"PDF Tools"},{"value":"3","label":"Workspace Types"},{"value":"Free","label":"Plan Available"},{"value":"Pro","label":"Advanced Workspaces"}]`,
			SectionTitle:        "What Makes PDFNest Different",
			SectionSubtitle:     "More than a PDF converter. A complete PDF workspace.",
			HighlightsJson:      `[{"title":"37+ PDF Tools","description":"Merge, split, compress, convert, secure, and organize PDFs with fast and reliable tools built for everyday work.","icon_type":"file"},{"title":"Virtual Document Studio","description":"Manage pages, watermarks, metadata, merging, compression, and document workflows from a single workspace.","icon_type":"layers"},{"title":"Interactive Canvas","description":"Create custom PDF layouts using drag-and-drop positioning, scaling controls, layer management, and visual editing.","icon_type":"pen"}]`,
			StudioTitle:         "Virtual Document Studio",
			StudioDescription:   "Manage document workflows visually from one workspace. Rotate pages, apply watermarks, update metadata, merge documents, compress files, and export professional PDFs.",
			StudioFeaturesJson:  `["Page management","Watermark controls","Metadata editing","Merge & compression workflows","Visual PDF workspace"]`,
			CanvasTitle:         "Interactive Canvas",
			CanvasDescription:   "Design PDFs visually using drag-and-drop tools. Position images, manage layers, resize content, and create custom page layouts before exporting.",
			CanvasFeaturesJson:  `["Drag & drop editing","Layer management","Position controls","Custom layouts","Professional PDF exports"]`,
			SecurityTitle:       "Privacy & Security",
			SecurityDescription: "Your files remain yours. Documents are processed securely, used only for the requested operation, and automatically removed after processing. We design PDFNest around simplicity, security, and performance without unnecessary complexity.",
			RoadmapTitle:        "Looking Ahead",
			RoadmapDescription:  "PDFNest continues to evolve with new tools, workspaces, and automation features.",
			RoadmapJson:         `["Advanced OCR","Team Workspaces","Batch Processing","Workflow Automation","Template Library","AI Assisted PDF Tools"]`,
			MissionTitle:        "Our Mission",
			MissionDescription:  "We believe powerful document tools should be accessible, intuitive, and fast. PDFNest was created to bring together everyday PDF utilities and advanced professional workspaces under one platform, allowing anyone to work with documents more efficiently without complicated software installations or unnecessary friction.",
		}
		config.DB.Create(&initialAbout)
	}
}
