// file: internal/content/site_content_seed.go
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
			AuthBannerFreeUsage:  "Usage: unit-based billing with 3-hour, daily, and monthly limits",
			AuthBannerFreeAction: "Upgrade to Pro",

			Feature1Title:       "Free Tier Included",
			Feature1Description: "Access baseline document utilities instantly with no upfront cost.",
			Feature2Title:       "Pro Ecosystem",
			Feature2Description: "Unlock high-performance processing, interactive canvas features, and larger workflows.",
			Feature3Title:       "Isolated Sandbox",
			Feature3Description: "Secure processing sandboxes compile your document jobs and clear data after completion.",

			SearchPlaceholder:      "Search tool modules (e.g., merge, watermark, encrypt)...",
			SearchScopeSuffix:      "tools matching search matrix scope",
			SearchEmptyTitle:       "No structural modules matched",
			SearchEmptyDescription: "Try checking spelling, tags, or clear filters.",

			PopularToolTitle:       "Merge PDF Documents Collectively",
			PopularToolDescription: "Combine separate files into one clean PDF in seconds.",
			PopularToolAction:      "Open Tool Module",

			CategoryOrganizeTitle: "Page Organization",
			CategoryOrganizeDesc:  "Merge, split, rotate, crop, and organize PDF pages with precision.",

			CategoryEditingTitle: "Document Editing",
			CategoryEditingDesc:  "Modify content, add annotations, signatures, watermarks, and page elements.",

			CategoryConvertTitle: "PDF Conversion",
			CategoryConvertDesc:  "Convert PDFs to and from Office documents, images, text, and other formats.",

			CategoryCreateTitle: "PDF Creation",
			CategoryCreateDesc:  "Create professional PDFs from documents, images, websites, code, and markdown.",

			CategorySecurityTitle: "Document Security",
			CategorySecurityDesc:  "Protect, unlock, and permanently redact sensitive PDF information.",

			CategoryOptimizeTitle: "Optimization & Repair",
			CategoryOptimizeDesc:  "Compress, repair, and optimize PDFs for storage, sharing, and printing.",

			CategoryStudioTitle: "PDF Studio",
			CategoryStudioDesc:  "Access an all-in-one workspace for advanced PDF editing and document management.",
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
			SpeedBulletPoints:   "Priority queues,Lower wait times,Flexible usage windows,Premium tools",

			FreeTitle:        "Free",
			FreePrice:        "0",
			FreeSubtitle:     "Perfect for occasional use",
			FreeBulletPoints: "Core PDF tools,Light OCR,Secure processing,Small unit allowance,3-hour / daily / monthly limits",

			PlusTitle:        "Plus",
			PlusPrice:        "9",
			PlusSubtitle:     "For active users",
			PlusBulletPoints: "Everything in Free,More units per window,Full OCR support,Better burst allowance,Priority processing",

			ProTitle:        "Pro",
			ProPrice:        "29",
			ProSubtitle:     "For professionals",
			ProBulletPoints: "Everything in Plus,Highest unit allowance,Advanced workflows,Large file handling,Premium workspace tools",

			SecurityTitle:    "Your files stay completely private",
			SecuritySubtitle: "Document security is built into the core architecture.",
			SecurityTags:     "Temporary processing,Secure transfers,Automatic cleanup,No permanent storage",

			CtaGuestTitle:   "Create a free account and start using PDFNest today.",
			CtaFreeTitle:    "Need more room to work?",
			CtaFreeSubtitle: "Upgrade to Plus or Pro for larger unit windows and heavier workflows.",
			CtaPlusTitle:    "Ready for bigger jobs?",
			CtaProTitle:     "You already have full access.",
			CtaProSubtitle:  "Your account can use the highest available unit windows and workflow limits.",

			FaqsJSON: `[
				{"q": "Is PDFNest free?", "a": "Yes! The Free Plan gives you access to core PDF tools with a limited unit allowance and separate 3-hour, daily, and monthly windows."},
				{"q": "How does billing work now?", "a": "PDFNest now uses units. Simple tools cost fewer units, heavy tools cost more, and some tools can charge extra based on pages or OCR work."},
				{"q": "What does Plus include?", "a": "Plus gives you more units, better burst capacity, and stronger support for OCR and larger jobs."},
				{"q": "What does Pro include?", "a": "Pro is the highest tier. It includes the largest unit allowance, the best burst capacity, and the most room for heavy workflows."},
				{"q": "Can I cancel anytime?", "a": "Absolutely. You can upgrade, downgrade, or cancel your active billing status inside your settings panel at any point."},
				{"q": "Are my files stored?", "a": "Never. Files are processed in temporary runtime sandboxes and cleaned up after processing."}
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
			HighlightsJson:      `[{"title":"37+ PDF Tools","description":"Merge, split, compress, convert, secure, and organize PDFs with fast and reliable tools built for everyday work.","icon_type":"file"},{"title":"Virtual Document Studio","description":"Manage pages, watermarks, metadata, merging, compression, and document workflows from a single workspace.","icon_type":"layers"},{"title":"Interactive Canvas","description":"Design PDFs visually using drag-and-drop positioning, scaling controls, layer management, and visual editing.","icon_type":"pen"}]`,
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
