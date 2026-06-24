package models

import (
	"time"
)

type DynamicToolItem struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	Title          string `gorm:"type:varchar(255);not null;uniqueIndex"`
	Description    string `gorm:"type:text;not null"`
	Href           string `gorm:"type:varchar(255);not null;uniqueIndex"`
	Category       string `gorm:"type:varchar(50);not null"`
	KeywordsJson   string `gorm:"type:text;default:'[]'"`
	SeoTitle       string `gorm:"type:varchar(255)"`
	SeoDescription string `gorm:"type:text"`
	Intent         string `gorm:"type:text"`
	RelatedJson    string `gorm:"type:text;default:'[]'"`
	FaqJson        string `gorm:"type:text;default:'[]'"`
	FeaturesJson   string `gorm:"type:text;default:'[]'"`
	IsNew          bool   `gorm:"default:false"`
	Accept         string `gorm:"type:varchar(255)"`
	Multiple       bool   `gorm:"default:false"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
