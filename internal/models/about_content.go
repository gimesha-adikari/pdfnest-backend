package models

import (
	"time"
)

type AboutPageContent struct {
	ID                  uint   `gorm:"primaryKey;autoIncrement"`
	HeroTag             string `gorm:"type:varchar(255);not null;default:'About PDFNest'"`
	HeroTitle           string `gorm:"type:varchar(255);not null;default:'Built For Modern PDF Workflows'"`
	HeroDescription     string `gorm:"type:text;not null"`
	StatsJson           string `gorm:"type:text;default:'[]'"`
	SectionTitle        string `gorm:"type:varchar(255);not null;default:'What Makes PDFNest Different'"`
	SectionSubtitle     string `gorm:"type:varchar(255);not null;default:'More than a PDF converter. A complete PDF workspace.'"`
	HighlightsJson      string `gorm:"type:text;default:'[]'"`
	StudioTitle         string `gorm:"type:varchar(255);not null;default:'Virtual Document Studio'"`
	StudioDescription   string `gorm:"type:text;not null"`
	StudioFeaturesJson  string `gorm:"type:text;default:'[]'"`
	CanvasTitle         string `gorm:"type:varchar(255);not null;default:'Interactive Canvas'"`
	CanvasDescription   string `gorm:"type:text;not null"`
	CanvasFeaturesJson  string `gorm:"type:text;default:'[]'"`
	SecurityTitle       string `gorm:"type:varchar(255);not null;default:'Privacy & Security'"`
	SecurityDescription string `gorm:"type:text;not null"`
	RoadmapTitle        string `gorm:"type:varchar(255);not null;default:'Looking Ahead'"`
	RoadmapDescription  string `gorm:"type:text;not null"`
	RoadmapJson         string `gorm:"type:text;default:'[]'"`
	MissionTitle        string `gorm:"type:varchar(255);not null;default:'Our Mission'"`
	MissionDescription  string `gorm:"type:text;not null"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
