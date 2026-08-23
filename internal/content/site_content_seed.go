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
			HeroBadgePlus:         "Plus Workspace Active",
			HeroBadgePro:          "Pro Workspace Active",
			HeroWelcomeBack:       "Welcome Back",
			HeroTitleGuest:        "PDF Workspace",
			HeroTitlePlus:         "Plus Workspace",
			HeroTitlePro:          "Pro Workspace",
			HeroSubtitleGuest:     "Edit, convert, secure, and organize PDFs online with advanced, cloud-native processing tools.",
			HeroSubtitleGuestBold: "Start free. Upgrade anytime.",

			AuthBannerProAccess:  "Capacity: High-allowance unit allocation for intensive processing",
			AuthBannerFreeUsage:  "Usage: 20 daily units • 8 per 3-hour window • 80 per month",
			AuthBannerFreeAction: "Upgrade to Plus",

			Feature1Title:       "Free Tier Included",
			Feature1Description: "Access core document utilities with daily processing units at zero upfront cost.",
			Feature2Title:       "High-Capacity Processing",
			Feature2Description: "Higher unit allowances across 3-hour, daily, and monthly windows for demanding workloads.",
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
			CategoryStudioDesc:  "Access an all-in-one workspace for visual PDF editing and document management.",
		}
		config.DB.Create(&homeContent)
	}

	var subCount int64
	config.DB.Model(&models.SubscribePageContent{}).Count(&subCount)
	if subCount > 0 {
		var existing models.SubscribePageContent
		if err := existing.ResetExistingData(config.DB); err != nil {
			panic(err)
		}
	}

	subContent := models.SubscribePageContent{
		ID:                1,
		HeroBadge:         "Transparent Computing Tiers",
		HeroTitle:         "Choose the Right Capacity for",
		HeroTitleGradient: "Your Document Workflows",
		HeroSubtitle:      "All tools, Studio, and privacy features are available on every plan. Upgrade for higher processing unit allowances and demanding workloads.",

		PremiumSectionTitle: "Processing Capacity Built for Every Workload",
		StudioTitle:         "More Processing Capacity",
		StudioDescription:   "Handle more document-processing work with higher 3-hour burst and daily unit allowances.",
		StudioBulletPoints:  "Higher daily unit allowances,3-hour burst capacity,Seamless workflow continuity,Predictable usage resets",
		CanvasTitle:         "Built for Demanding Documents",
		CanvasDescription:   "Resource-intensive operations scale transparently with document complexity, page count, and images.",
		CanvasBulletPoints:  "Page-weighted unit cost scaling,Multi-page batch conversions,Intensive OCR text extraction,High-volume document compilation",
		SpeedTitle:          "Room for Heavy Workloads",
		SpeedDescription:    "Higher tiers provide substantially more processing capacity for regular and heavy multi-document jobs.",
		SpeedBulletPoints:   "100 to 400 daily processing units,Extended page duplication limits,Optional credit top-ups,7-day free trial on Plus & Pro",

		FreeTitle:        "Free",
		FreePrice:        "0",
		FreeSubtitle:     "For everyday, occasional document tasks",
		FreeBulletPoints: "Access to all 39+ PDF tools,Studio workspace access,20 processing units per day,8 units per 3-hour window,80 units per month allowance",

		PlusTitle:        "Plus",
		PlusMonthlyPrice: "4.99",
		PlusYearlyPrice:  "49.99",
		PlusSubtitle:     "For active users and frequent document tasks",
		PlusBulletPoints: "Everything in Free,100 processing units per day,50 units per 3-hour window,500 units per month allowance,Higher capacity for multi-page jobs",

		ProTitle:        "Pro",
		ProMonthlyPrice: "9.99",
		ProYearlyPrice:  "99.99",
		ProSubtitle:     "For power users and demanding batch workloads",
		ProBulletPoints: "Everything in Plus,400 processing units per day,150 units per 3-hour window,2000 units per month allowance,Maximum capacity for heavy OCR and conversion jobs,Extended page duplication limits",

		TrialText: "7-day free trial",

		SecurityTitle:    "Your files stay completely private",
		SecuritySubtitle: "Document security is built into the core architecture.",
		SecurityTags:     "Temporary processing,Secure transfers,Automatic cleanup,No permanent storage",

		CtaGuestTitle: "Create a free account and start using Platen PDF today.",

		CtaFreeTitle:    "Need more power?",
		CtaFreeSubtitle: "Choose monthly or yearly billing and start with a 7-day free trial.",

		CtaPlusTitle:    "Need even higher limits?",
		CtaPlusSubtitle: "Upgrade to Pro for 400 daily units and maximum processing capacity.",

		CtaProTitle:    "You're on our most powerful plan.",
		CtaProSubtitle: "Manage your subscription anytime from your account settings.",

		FaqsJSON: `[
    {
        "q":"Is Platen PDF free?",
        "a":"Yes. The Free plan includes all 39+ PDF tools and the Studio workspace with 20 processing units per day."
    },
    {
        "q":"How do processing units work?",
        "a":"Each tool operation consumes units based on document size and complexity. Simple operations use 1–2 units, while complex OCR or conversions consume units proportionally to page count."
    },
    {
        "q":"Do Plus and Pro include a free trial?",
        "a":"Yes. Every new Plus and Pro subscription includes a 7-day free trial."
    },
    {
        "q":"Can I choose monthly or yearly billing?",
        "a":"Yes. Both Plus and Pro are available with monthly and yearly billing options."
    },
    {
        "q":"Can I cancel during the trial?",
        "a":"Yes. You can cancel at any time during the trial and you won't be charged. Your access continues until the trial ends."
    },
    {
        "q":"What happens if I cancel my subscription?",
        "a":"Your subscription stays active until the end of the current billing period or trial. After that, your account automatically returns to the Free plan."
    },
    {
        "q":"Are my files stored?",
        "a":"No. Files are processed temporarily in ephemeral sandboxes and automatically deleted after processing."
    }
]`,
	}

	config.DB.Create(&subContent)

	var count int64
	config.DB.Model(&models.AboutPageContent{}).Count(&count)
	if count == 0 {
		initialAbout := models.AboutPageContent{
			ID:                  1,
			HeroTag:             "About Platen PDF",
			HeroTitle:           "Built For Modern PDF Workflows",
			HeroDescription:     "Platen PDF combines powerful PDF tools, visual workspaces, and secure cloud processing into a single platform. Whether you're editing a document, creating a portfolio, preparing reports, or managing complex PDF workflows, Platen PDF helps you work faster and smarter.",
			StatsJson:           `[{"value":"39+","label":"PDF Tools"},{"value":"100%","label":"Privacy First"},{"value":"Free","label":"Plan Available"},{"value":"Pro","label":"High-Capacity Plans"}]`,
			SectionTitle:        "What Makes Platen PDF Different",
			SectionSubtitle:     "More than a PDF converter. A complete PDF workspace.",
			HighlightsJson:      `[{"title":"37+ PDF Tools","description":"Merge, split, compress, convert, secure, and organize PDFs with fast and reliable tools built for everyday work.","icon_type":"file"},{"title":"Virtual Document Studio","description":"Manage pages, watermarks, metadata, merging, compression, and document workflows from a single workspace.","icon_type":"layers"},{"title":"Interactive Canvas","description":"Design PDFs visually using drag-and-drop positioning, scaling controls, layer management, and visual editing.","icon_type":"pen"}]`,
			StudioTitle:         "Virtual Document Studio",
			StudioDescription:   "Manage document workflows visually from one workspace. Rotate pages, apply watermarks, update metadata, merge documents, compress files, and export professional PDFs.",
			StudioFeaturesJson:  `["Page management","Watermark controls","Metadata editing","Merge & compression workflows","Visual PDF workspace"]`,
			CanvasTitle:         "Interactive Canvas",
			CanvasDescription:   "Design PDFs visually using drag-and-drop tools. Position images, manage layers, resize content, and create custom page layouts before exporting.",
			CanvasFeaturesJson:  `["Drag & drop editing","Layer management","Position controls","Custom layouts","Professional PDF exports"]`,
			SecurityTitle:       "Privacy & Security",
			SecurityDescription: "Your files remain yours. Documents are processed securely, used only for the requested operation, and automatically removed after processing. We design Platen PDF around simplicity, security, and performance without unnecessary complexity.",
			RoadmapTitle:        "Looking Ahead",
			RoadmapDescription:  "Platen PDF continues to evolve with new tools, workspaces, and automation features.",
			RoadmapJson:         `["Advanced OCR","Team Workspaces","Batch Processing","Workflow Automation","Template Library","AI Assisted PDF Tools"]`,
			MissionTitle:        "Our Mission",
			MissionDescription:  "We believe powerful document tools should be accessible, intuitive, and fast. Platen PDF was created to bring together everyday PDF utilities and advanced professional workspaces under one platform, allowing anyone to work with documents more efficiently without complicated software installations or unnecessary friction.",
		}
		config.DB.Create(&initialAbout)
	}
}
